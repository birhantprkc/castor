// Package ffmpeg runs ffmpeg processes with typed outputs and stderr
// forensics. It contains no pipeline policy: arg builders are pure functions,
// and the runner only manages pipes, the stderr fan-out, and process exit. What a
// process reported about itself is a value (Evidence, and the samples on its
// progress feed); what any of it means is decided elsewhere.
package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// stderrTailCapacity bounds the number of recent ffmpeg stderr lines we keep
// for surfacing on failure. ffmpeg emits a startup metadata burst (~20 lines)
// plus one progress line per second; 128 covers the burst plus roughly two
// minutes of progress, so the actual error message survives.
const stderrTailCapacity = 128

// The extra output fds castor routes ffmpeg outputs to, and what travels on each.
// Which fd carries what is a contract between an argument builder in this package
// (see PullArgs and EncodeArgs) and the runner that opens and drains the pipe, so
// both ends read it here instead of from a bare number at either end. Pointing
// -progress at an fd the parent never opened is not a missing feed, it is "Failed
// to open progress URL pipe:3: Bad file descriptor" and a process that never runs.
//
// -progress takes the FIRST extra fd because it is the one output every invocation
// has. os/exec hands ExtraFiles[0] to the child as fd 3, so a PCM tee on fd 4
// cannot exist without fd 3 while a progress feed on its own can.
const (
	firstExtraFD = 3
	progressFD   = 3
	pcmFD        = 4
)

// pipeURL spells an fd the way ffmpeg's pipe protocol takes it.
func pipeURL(fd int) string { return "pipe:" + strconv.Itoa(fd) }

// noExitStatus is the ExitStatus of a process that has no status to report: it has
// not been waited on yet, or castor killed it. A classification rule must not read
// it as an exit, because the stall this value describes is exactly the case where
// ffmpeg's own error path never ran (see StderrTail).
const noExitStatus = -1

// Process is a running ffmpeg invocation.
type Process struct {
	// Stdout is the primary output (pipe:1).
	Stdout io.ReadCloser

	cmd *exec.Cmd

	// extra are the extra output pipes in fd order, starting at firstExtraFD, as
	// many as WithExtraPipes asked for.
	extra []io.ReadCloser

	// lines fans every stderr line out to the observers below and to any the caller
	// registers. Retention and detection are two subscribers rather than one welded
	// type because a marker whose cause sat a hundred lines deep used to be reachable
	// only by whoever also owned the ring buffer.
	lines   *fanout
	tail    *ringTail
	markers *markerWatch

	// status is the exit code, published by Wait so Evidence can be read from another
	// goroutine without racing os/exec's ProcessState.
	status atomic.Int64
}

type startConfig struct {
	stdin      io.Reader
	extraPipes int
	workDir    string
}

type StartOption func(*startConfig)

// WithStdin feeds r to ffmpeg's stdin (pipe:0 input).
func WithStdin(r io.Reader) StartOption {
	return func(c *startConfig) { c.stdin = r }
}

// WithWorkDir runs ffmpeg with dir as its working directory, so a muxer writing
// relative output files (the HLS playlist and segments) lands them there.
func WithWorkDir(dir string) StartOption {
	return func(c *startConfig) { c.workDir = dir }
}

// WithExtraPipes opens n extra output pipes for the child, fd 3 upward, in the
// order the fd constants above name them. The argument builder decides how many a
// command line needs (see PullOptions.ExtraPipes and EncodeExtraPipes) because it
// is what routes the outputs; a caller counting them itself is a second opinion
// that can disagree with the flags.
//
// Every pipe opened here must be drained for as long as the process runs. ffmpeg
// writes to them with blocking writes, so an unread pipe stops the process dead
// once the kernel buffer fills, with no exit status and no stderr line to say why.
func WithExtraPipes(n int) StartOption {
	return func(c *startConfig) { c.extraPipes = n }
}

// Start launches ffmpeg at path with args. The process is killed when ctx is
// cancelled.
func Start(ctx context.Context, path string, args []string, opts ...StartOption) (*Process, error) {
	var cfg startConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = cfg.stdin
	cmd.Dir = cfg.workDir

	var (
		extraRead  []io.ReadCloser
		extraWrite []*os.File
	)
	closeExtra := func() {
		for _, r := range extraRead {
			_ = r.Close()
		}
		for _, w := range extraWrite {
			_ = w.Close()
		}
	}
	for range cfg.extraPipes {
		r, w, err := os.Pipe()
		if err != nil {
			closeExtra()
			return nil, fmt.Errorf("extra output pipe: %w", err)
		}
		extraRead = append(extraRead, r)
		extraWrite = append(extraWrite, w)
	}
	cmd.ExtraFiles = extraWrite // ExtraFiles[i] is fd firstExtraFD+i in the child

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closeExtra()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		closeExtra()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		closeExtra()
		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}
	// Close our copies of the write ends so each extra reader sees EOF when ffmpeg
	// exits rather than blocking on a pipe this process is still holding open.
	for _, w := range extraWrite {
		_ = w.Close()
	}

	p := &Process{
		Stdout:  stdout,
		cmd:     cmd,
		extra:   extraRead,
		lines:   &fanout{},
		tail:    newTail(stderrTailCapacity),
		markers: &markerWatch{},
	}
	p.status.Store(noExitStatus)
	// Registered before the drain starts, so neither the retained tail nor the marker
	// scan can miss a line of the startup burst.
	p.lines.add(p.tail)
	p.lines.add(p.markers)
	go drainStderr(ctx, stderr, p.lines)

	return p, nil
}

// extraAt returns the reader for one extra output fd, nil when the process was not
// started with a pipe for it.
func (p *Process) extraAt(fd int) io.ReadCloser {
	i := fd - firstExtraFD
	if i < 0 || i >= len(p.extra) {
		return nil
	}
	return p.extra[i]
}

// ProgressFeed is ffmpeg's -progress key=value feed (see WatchProgress for what is
// on it). It is nil only when the process was started without an extra pipe, which
// for a command line this package built cannot happen: both builders emit -progress
// unconditionally, because a reader that produces no telemetry leaves a starving
// cast with nothing to judge but a byte count.
func (p *Process) ProgressFeed() io.ReadCloser { return p.extraAt(progressFD) }

// PCMFeed is the mono s16le audio tee the transcriber consumes, nil unless the
// command line asked for one (see PullOptions.PCM). Its consumer must keep draining
// it until EOF: backpressure here throttles the whole read.
func (p *Process) PCMFeed() io.ReadCloser { return p.extraAt(pcmFD) }

// Wait blocks until the process exits and returns its exit error, if any.
// Forensics are the caller's call: use StderrTail or LogStderrTail to
// surface the failure reason when the exit was not self-inflicted.
//
// There is deliberately no broadcast channel alongside this. Every stage that
// runs an ffmpeg process already owns the goroutine that calls Wait and
// publishes the terminal error on its own terms (the pull's Done, the delivery
// driver's result channel), so a second settled-signal on Process would be a
// second thing to keep in agreement with the first.
func (p *Process) Wait() error {
	err := p.cmd.Wait()
	// Published here rather than read off ProcessState on demand: Evidence is asked
	// for from whichever goroutine is diagnosing the failure, and os/exec offers no
	// synchronisation for that field.
	p.status.Store(int64(p.cmd.ProcessState.ExitCode()))
	return err
}

// Kill signals the process to stop immediately. It is idempotent and safe to
// call after the process has already exited (the error is ignored), so callers
// can defer it as unconditional teardown on paths where context cancellation is
// not the only way the encoder must stop (the HLS serve path, whose output is
// files rather than a pipe that would EPIPE on close). Reap it with Wait.
func (p *Process) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// StderrTail returns the most recent stderr lines, retained even while the
// process is still running. This is what explains a stall after the process
// has been killed by context cancellation: its own error path never runs.
func (p *Process) StderrTail() []string {
	return p.tail.snapshot()
}

// Observer receives every stderr line as it is read. Process drives exactly this
// one method, so the retained tail, the unplayable-output markers and anything that
// classifies a failure are registrations rather than edits to one private type.
//
// Nothing registered here gates anything. Prose is not a contract (see the carriage
// package doc), so a marker may only NARROW a classification the exit status already
// reached, never enable one: a wording change in ffmpeg costs diagnostic sharpness
// and can never cost a cast.
type Observer interface {
	Observe(line string)
}

// Observe registers o for every stderr line read from now on. A late registration
// does not replay: what already went past is in the retained tail, which is what
// Evidence carries, and replaying a marker to a subscriber that arrived after the
// fact would report a line as freshly seen.
func (p *Process) Observe(o Observer) { p.lines.add(o) }

// Evidence is what a finished (or killed) process can be asked about, as one value
// rather than three calls, so a rule reads facts instead of holding a Process.
type Evidence struct {
	// ExitStatus is the process's exit code, or noExitStatus when there is none to
	// read: it has not been waited on, or castor killed it. The second case is the
	// one a stall diagnosis is built from, so it must not be mistaken for an exit.
	ExitStatus int

	// Markers are the unplayable-output markers this process printed, in the order
	// they first appeared (see silentFailures). They sharpen a verdict the status
	// already reached; on their own they mean only that ffmpeg said something known.
	Markers []string

	// Lines is the retained stderr tail: the evidence a human reads when no rule
	// recognised what happened.
	Lines []string
}

// Evidence collects what this process left behind. Read it after Wait, where the
// exit status exists; before that the status is noExitStatus and the markers and
// lines are whatever has arrived so far, which is exactly what a stall report wants.
func (p *Process) Evidence() Evidence {
	return Evidence{
		ExitStatus: int(p.status.Load()),
		Markers:    p.markers.snapshot(),
		Lines:      p.tail.snapshot(),
	}
}

// LogStderrTail emits every retained stderr line at WARN under msg.
func (p *Process) LogStderrTail(ctx context.Context, msg string) {
	for _, line := range p.StderrTail() {
		slog.WarnContext(ctx, msg, "line", line)
	}
}

// drainStderr reads stderr line-by-line, logging each at DEBUG and handing it to
// the observers. It reads to EOF whatever anyone does with the lines, because the
// stderr pipe is one of the pipes that stops ffmpeg dead when nobody empties it.
func drainStderr(ctx context.Context, r io.Reader, to Observer) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		to.Observe(line)
		slog.DebugContext(ctx, "ffmpeg", "line", line)
	}
	if err := scanner.Err(); err != nil {
		slog.WarnContext(ctx, "ffmpeg stderr scanner error", "error", err)
	}
}

// fanout delivers each line to every registered observer, in registration order. It
// is itself an Observer, so the drain above knows about one destination however many
// there turn out to be.
//
// A slow observer holds up the drain and therefore ffmpeg's stderr, so what is
// registered here belongs to the class of work that inspects a line and returns: a
// substring test, an append, a counter.
type fanout struct {
	mu        sync.Mutex
	observers []Observer
}

func (f *fanout) add(o Observer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observers = append(f.observers, o)
}

func (f *fanout) Observe(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.observers {
		o.Observe(line)
	}
}

// silentFailure is one stderr line that means the output is not playable,
// whatever the exit status says.
type silentFailure struct {
	marker string
	reason string
}

// silentFailures are stderr lines ffmpeg emits while exiting 0 and producing
// output that is not playable, so a caller folding them into the exit status
// stops reporting a healthy cast.
//
// This is the last place in castor that reads what ffmpeg printed, and it earns
// that only because there is nothing else to read: the shapes here exit 0 having
// produced a plausible-looking artifact, so neither the status nor the output
// betrays them. Nothing here drives a strategy. Whether a container refused a
// track, and which axis to re-encode about it, is decided from the artifact by
// the carriage package, precisely because the ffmpeg CLI offers no
// machine-readable error channel and prose is not a contract.
//
// A container refusing a codec is deliberately absent: that shape is prevented
// rather than detected (the carriage tables refuse the copy), and reporting it as
// terminal here would kill the cast before the recovery ran.
//
// So the table below holds two rows and not a matrix. One is genuinely silent, an
// ADTS repack aimed at an in-band container that exits 0 with the packets
// discarded; the other exits non-zero and is kept because catching it on the first
// line names the fix. Watching for lines needs no codec allow-list, so it stays
// correct as ffmpeg's muxers change, and the copy adaptation tables already make
// both unreachable on the paths castor plans. This is the net under the path it
// cannot plan (the read-once pull, which runs before any probe exists) and under
// every codec nobody has characterised yet. It observes; it gates nothing.
var silentFailures = []silentFailure{
	{
		// aac_adtstoasc pointed at an in-band container with an ADTS input: the
		// muxer rejects every packet and says so once per packet, and the process
		// still exits 0. 189 packets became 8, 470 became 61, and nothing decodes.
		marker: "AAC bitstream not in ADTS format and extradata missing",
		reason: "an audio repack was applied toward a container that frames its streams in band, and the muxer discarded the packets",
	},
	{
		// This one does exit non-zero, so it is not strictly a silent failure. It is
		// here because catching it on the first line is free and it names the exact
		// fix, which makes a stderr tail immediately actionable.
		marker: "Malformed AAC bitstream detected",
		reason: "an ADTS-framed AAC track reached a container that declares its decoder configuration up front, with no repack",
	},
}

// SilentFailure returns a non-nil error once ffmpeg has printed a line that means
// the output is broken regardless of the exit status, or nil. It is safe to call
// while the process is still running and after it has exited; callers fold it
// into the process's terminal error with cmp.Or so a clean exit with a poisoned
// output still fails the cast.
func (p *Process) SilentFailure() error { return p.markers.failure() }

// ringTail keeps the most recent N stderr lines. It retains and judges nothing: the
// marker scan is a separate observer, because the tail is bounded and the thing that
// must not be lost is the marker, not its position in a buffer. The AVCC desync's
// cause survived only as a line at the edge of this window.
type ringTail struct {
	mu  sync.Mutex
	buf []string
	cap int
}

func newTail(capacity int) *ringTail {
	return &ringTail{buf: make([]string, 0, capacity), cap: capacity}
}

func (t *ringTail) Observe(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) == t.cap {
		t.buf = t.buf[1:]
	}
	t.buf = append(t.buf, line)
}

func (t *ringTail) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.buf)
}

// markerWatch watches every line on its way past for the unplayable-output markers.
// It outlives the ring tail on purpose: a marker printed during a startup burst that
// a two-minute run then scrolls out of the buffer is still the reason that run's
// output does not play.
type markerWatch struct {
	mu sync.Mutex
	// silent is the first silent-failure line seen, nil until one appears. The
	// FIRST is kept rather than the last because ffmpeg repeats these and the first
	// is the one whose cause is still on screen.
	silent error
	// seen are the markers that have fired, in first-appearance order and without
	// repeats, since ffmpeg prints some of these once per packet.
	seen []string
}

func (w *markerWatch) Observe(line string) {
	for _, f := range silentFailures {
		if !strings.Contains(line, f.marker) {
			continue
		}
		w.mu.Lock()
		if !slices.Contains(w.seen, f.marker) {
			w.seen = append(w.seen, f.marker)
		}
		if w.silent == nil {
			w.silent = fmt.Errorf("ffmpeg produced unplayable output: %s (%s)", f.reason, line)
		}
		w.mu.Unlock()
		return
	}
}

func (w *markerWatch) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.silent
}

func (w *markerWatch) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.seen)
}
