// Package ffmpeg runs ffmpeg with typed outputs; policy lives elsewhere (SOURCE, DEVICE, OUTPUT).
package ffmpeg

import (
	"bufio"
	"context"
	"errors"
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
	"syscall"

	"github.com/stupside/castor/internal/media"
)

// stderrTailCapacity bounds retained lines: startup burst (~20) plus two minutes of progress.
const stderrTailCapacity = 128

// Extra output fd constants: -progress fd 3 is the one output every invocation has.
const (
	firstExtraFD = 3
	progressFD   = 3
	pcmFD        = 4
)

// pipeURL spells an fd the way ffmpeg's pipe protocol takes it.
func pipeURL(fd int) string { return "pipe:" + strconv.Itoa(fd) }

// noExitStatus = no status yet (not waited) or castor's kill ended it (stall: ffmpeg's error path never ran).
const noExitStatus = -1

// signalExitBase is added to signal numbers per shell convention (driver segfault or OOM killer).
const signalExitBase = 128

type Process struct {
	// Stdout is the primary output (pipe:1).
	Stdout io.ReadCloser

	cmd *exec.Cmd

	// extra are the extra output pipes in fd order, starting at firstExtraFD.
	extra []io.ReadCloser

	// lines fans stderr to tail + markers (marker needs post-deadline lines tail drops).
	lines   *fanout
	tail    *ringTail
	markers *markerWatch

	// status is the exit code; atomics avoid racing ProcessState.
	status atomic.Int64

	// stopped records that castor sent its kill signal (see exitStatus).
	stopped *atomic.Bool

	// sample is the latest -progress block, stored by the progress drain goroutine.
	sample atomic.Pointer[media.Progress]

	// drained is closed once the progress feed is read to EOF.
	drained chan struct{}

	// scanned is closed once stderr is read to EOF.
	scanned chan struct{}

	// teed is closed once the PCM feed is copied to EOF (at once when there is none).
	teed chan struct{}
}

type startConfig struct {
	stdin    io.Reader
	workDir  string
	progress func(media.Progress)
	pcm      io.Writer
}

type StartOption func(*startConfig)

func WithStdin(r io.Reader) StartOption {
	return func(c *startConfig) { c.stdin = r }
}

// WithWorkDir runs ffmpeg with dir as its working directory for relative output files (HLS).
func WithWorkDir(dir string) StartOption {
	return func(c *startConfig) { c.workDir = dir }
}

// WithProgress calls step once per -progress sample (the feed has exactly one reader).
func WithProgress(step func(media.Progress)) StartOption {
	return func(c *startConfig) { c.progress = step }
}

// WithPCM copies the command's PCM tee into w until ffmpeg closes it; Wait joins the copy, w stays the caller's.
func WithPCM(w io.Writer) StartOption {
	return func(c *startConfig) { c.pcm = w }
}

// Start launches the command at path. The process is killed when ctx is cancelled.
func Start(ctx context.Context, path string, command Command, opts ...StartOption) (*Process, error) {
	var cfg startConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	// An unread tee blocks ffmpeg once the pipe fills, and a writer with no tee waits forever.
	if tees := command.ExtraPipes > pcmFD-firstExtraFD; tees != (cfg.pcm != nil) {
		return nil, fmt.Errorf("a PCM tee routed (%t) and a PCM consumer given (%t) disagree", tees, cfg.pcm != nil)
	}

	cmd := exec.CommandContext(ctx, path, command.Args...)
	stopped := new(atomic.Bool)
	cmd.Cancel = func() error {
		stopped.Store(true)
		return cmd.Process.Kill()
	}
	cmd.Stdin = cfg.stdin
	cmd.Dir = cfg.workDir

	var (
		extraRead   []io.ReadCloser
		extraWrite  []*os.File
		stderrRead  *os.File
		stderrWrite *os.File
	)
	closeExtra := func() {
		for _, r := range extraRead {
			_ = r.Close()
		}
		for _, w := range extraWrite {
			_ = w.Close()
		}
		for _, f := range []*os.File{stderrRead, stderrWrite} {
			if f != nil {
				_ = f.Close()
			}
		}
	}
	for range command.ExtraPipes {
		r, w, err := os.Pipe()
		if err != nil {
			closeExtra()
			return nil, fmt.Errorf("extra output pipe: %w", err)
		}
		extraRead = append(extraRead, r)
		extraWrite = append(extraWrite, w)
	}
	cmd.ExtraFiles = extraWrite

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closeExtra()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// Our own pipe: os/exec closes it at Wait, losing buffered tail (ffmpeg's error path).
	stderrRead, stderrWrite, err = os.Pipe()
	if err != nil {
		closeExtra()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stderr = stderrWrite
	if err := cmd.Start(); err != nil {
		closeExtra()
		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}
	// Close our copies so each reader sees EOF when ffmpeg exits.
	for _, w := range extraWrite {
		_ = w.Close()
	}
	_ = stderrWrite.Close()

	p := &Process{
		Stdout:  stdout,
		cmd:     cmd,
		extra:   extraRead,
		lines:   &fanout{},
		tail:    newTail(stderrTailCapacity),
		markers: &markerWatch{},
		scanned: make(chan struct{}),
		stopped: stopped,
	}
	p.status.Store(noExitStatus)
	p.lines.add(p.tail)
	p.lines.add(p.markers)
	go func() {
		defer close(p.scanned)
		defer func() { _ = stderrRead.Close() }()
		drainStderr(ctx, stderrRead, p.lines)
	}()
	p.followProgress(cfg.progress)
	p.tee(cfg.pcm)

	return p, nil
}

func (p *Process) tee(w io.Writer) {
	p.teed = make(chan struct{})
	feed := p.extraAt(pcmFD)
	if feed == nil {
		close(p.teed)
		return
	}
	go func() {
		defer close(p.teed)
		defer func() { _ = feed.Close() }()
		if _, err := io.Copy(w, feed); err != nil {
			// A consumer that quit must not stall ffmpeg on a full pipe.
			_, _ = io.Copy(io.Discard, feed)
		}
	}()
}

func (p *Process) followProgress(step func(media.Progress)) {
	p.drained = make(chan struct{})
	feed := p.progressFeed()
	if feed == nil {
		close(p.drained)
		return
	}
	go func() {
		defer close(p.drained)
		defer func() { _ = feed.Close() }()
		WatchProgress(feed, func(sample media.Progress) {
			p.sample.Store(&sample)
			if step != nil {
				step(sample)
			}
		})
	}()
}

func (p *Process) extraAt(fd int) io.ReadCloser {
	i := fd - firstExtraFD
	if i < 0 || i >= len(p.extra) {
		return nil
	}
	return p.extra[i]
}

// progressFeed is ffmpeg's -progress feed (has exactly one reader for its life).
func (p *Process) progressFeed() io.ReadCloser { return p.extraAt(progressFD) }

// Progress returns the latest sample. Zero value = not yet muxed; whole pair is comparable.
func (p *Process) Progress() media.Progress {
	if s := p.sample.Load(); s != nil {
		return *s
	}
	return media.Progress{}
}

func (p *Process) Wait() error {
	err := p.cmd.Wait()
	// Publish exit status for Evidence reads from other goroutines.
	p.status.Store(int64(p.exitStatus()))
	// Then join progress (last sample still in pipe); step writes to work dir caller will remove.
	<-p.drained
	// And join stderr drain (ffmpeg's error path is still in the pipe).
	<-p.scanned
	<-p.teed
	return err
}

// Kill signals the process to stop. It is idempotent and safe after exit.
func (p *Process) Kill() {
	if p.cmd.Process != nil {
		p.stopped.Store(true)
		_ = p.cmd.Process.Kill()
	}
}

// exitStatus reads the reaped process status; only castor's own SIGKILL leaves none.
func (p *Process) exitStatus() int {
	state := p.cmd.ProcessState
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return state.ExitCode()
	}
	// Teardown kills before it waits, so a crash already reaped still names its own signal.
	if ws.Signal() == syscall.SIGKILL && p.stopped.Load() {
		return noExitStatus
	}
	return signalExitBase + int(ws.Signal())
}

// StderrTail returns the most recent stderr lines (kept even while process runs).
func (p *Process) StderrTail() []string {
	return p.tail.snapshot()
}

type observer interface {
	Observe(line string)
}

type Evidence struct {
	// ExitStatus is the process exit code (noExitStatus = not reaped or castor killed it).
	ExitStatus int

	// Markers are unplayable-output markers printed by ffmpeg, in first-appearance order.
	Markers []string

	Lines []string
}

// Evidence collects what the process left behind (read after Wait for complete status).
func (p *Process) Evidence() Evidence {
	return Evidence{
		ExitStatus: int(p.status.Load()),
		Markers:    p.markers.snapshot(),
		Lines:      p.tail.snapshot(),
	}
}

func (p *Process) LogStderrTail(ctx context.Context, msg string) {
	for _, line := range p.StderrTail() {
		slog.WarnContext(ctx, msg, "line", line)
	}
}

// stderrLineBuffer bounds one line read; Scanner would abandon over-long lines (blocks ffmpeg).
const stderrLineBuffer = 16 << 10

// drainStderr reads stderr line-by-line, handing each to observers; must read to EOF.
func drainStderr(ctx context.Context, r io.Reader, to observer) {
	buffered := bufio.NewReaderSize(r, stderrLineBuffer)
	for {
		chunk, err := buffered.ReadSlice('\n')
		if line := strings.TrimRight(string(chunk), "\r\n"); line != "" {
			to.Observe(line)
			slog.DebugContext(ctx, "ffmpeg", "line", line)
		}
		switch {
		case err == nil, errors.Is(err, bufio.ErrBufferFull):
			// ErrBufferFull: over-long line continues; marker straddled is lost but pipe keeps moving.
			continue
		case errors.Is(err, io.EOF), errors.Is(err, os.ErrClosed):
			return
		default:
			slog.WarnContext(ctx, "ffmpeg stderr read error", "error", err)
			return
		}
	}
}

type fanout struct {
	mu        sync.Mutex
	observers []observer
}

func (f *fanout) add(o observer) {
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

// silentFailure is one stderr line that means the output is not playable.
type silentFailure struct {
	marker string
	reason string
}

// silentFailures are markers ffmpeg emits on exit 0 with unplayable output.
var silentFailures = []silentFailure{
	{
		// aac_adtstoasc with ADTS input: muxer rejects every packet, output unplayable.
		marker: "AAC bitstream not in ADTS format and extradata missing",
		reason: "an audio repack was applied toward a container that frames its streams in band, and the muxer discarded the packets",
	},
	{
		// Exits non-zero; free to catch early.
		marker: "Malformed AAC bitstream detected",
		reason: "an ADTS-framed AAC track reached a container that declares its decoder configuration up front, with no repack",
	},
}

// SilentFailure returns a non-nil error once ffmpeg printed unplayable-output line, or nil.
func (p *Process) SilentFailure() error { return p.markers.failure() }

// ringTail keeps the most recent N stderr lines (marker watch is separate; needs non-tail lines).
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

// markerWatch scans every line for unplayable-output markers (survives tail scrolling).
type markerWatch struct {
	mu sync.Mutex
	// silent is the FIRST silent-failure line (nil until one appears).
	silent error
	// seen are markers that fired, in order and without repeats.
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
