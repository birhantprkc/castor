// Package ffmpeg runs the ffmpeg tools: their processes, what they report, and the flags every reader opens a source with.
package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"sync/atomic"
	"syscall"

	"github.com/stupside/castor/internal/media"
)

// Extra output fd constants: -progress fd 3 is the one output every invocation has.
const (
	firstExtraFD = 3
	progressFD   = 3
	pcmFD        = 4
)

// pipeURL spells an fd the way ffmpeg's pipe protocol takes it.
func pipeURL(fd int) string { return "pipe:" + strconv.Itoa(fd) }

// The pipes a command reads its input from and routes its outputs to; Start carries the -progress feed and the PCM tee over loopback.
var (
	StdinPipe    = pipeURL(0)
	StdoutPipe   = pipeURL(1)
	ProgressPipe = pipeURL(progressFD)
	PCMPipe      = pipeURL(pcmFD)
)

// noExitStatus = no status yet (not waited) or castor's kill ended it (stall: ffmpeg's error path never ran).
const noExitStatus = -1

// signalExitBase is added to signal numbers per shell convention (driver segfault or OOM killer).
const signalExitBase = 128

type Process struct {
	// Stdout is the primary output (pipe:1).
	Stdout io.ReadCloser

	cmd *exec.Cmd

	// extra are the side outputs in pipe order, starting at firstExtraFD.
	extra []*loopback

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

// Options is what a process runs with beyond its command line; each zero value means none.
type Options struct {
	Stdin io.Reader
	// WorkDir is where relative output files (HLS) are written.
	WorkDir string
	// Progress is called once per -progress sample.
	Progress func(media.Progress)
	// PCM receives the command's PCM tee until ffmpeg closes it; Wait joins the copy, the writer stays the caller's.
	PCM io.Writer
}

// Start launches the command at path. The process is killed when ctx is cancelled.
func Start(ctx context.Context, path string, command Command, opts Options) (*Process, error) {
	// An unread tee blocks ffmpeg once the pipe fills, and a writer with no tee waits forever.
	if tees := command.ExtraPipes > pcmFD-firstExtraFD; tees != (opts.PCM != nil) {
		return nil, fmt.Errorf("a PCM tee routed (%t) and a PCM consumer given (%t) disagree", tees, opts.PCM != nil)
	}

	var (
		extra       []*loopback
		stderrRead  *os.File
		stderrWrite *os.File
	)
	closeExtra := func() {
		for _, l := range extra {
			_ = l.Close()
		}
		for _, f := range []*os.File{stderrRead, stderrWrite} {
			if f != nil {
				_ = f.Close()
			}
		}
	}
	args := slices.Clone(command.Args)
	for i := range command.ExtraPipes {
		l, err := newLoopback()
		if err != nil {
			closeExtra()
			return nil, err
		}
		extra = append(extra, l)
		// A side output is routed to its pipe number in the argv, and travels over the loopback instead.
		for j, arg := range args {
			if arg == pipeURL(firstExtraFD+i) {
				args[j] = l.url
			}
		}
	}

	cmd := exec.CommandContext(ctx, path, args...)
	stopped, scanned := new(atomic.Bool), make(chan struct{})
	cmd.Cancel = func() error { return kill(cmd.Process, scanned, stopped) }
	cmd.Stdin = opts.Stdin
	cmd.Dir = opts.WorkDir

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
	_ = stderrWrite.Close()

	p := &Process{
		Stdout:  stdout,
		cmd:     cmd,
		extra:   extra,
		lines:   &fanout{},
		tail:    newTail(),
		markers: &markerWatch{},
		scanned: scanned,
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
	p.followProgress(opts.Progress)
	p.tee(opts.PCM)

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
	feed := p.extraAt(progressFD)
	if feed == nil {
		close(p.drained)
		return
	}
	go func() {
		defer close(p.drained)
		defer func() { _ = feed.Close() }()
		watchProgress(feed, func(sample media.Progress) {
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
	// An ffmpeg that died before opening a side output never will, so its reader is released to EOF.
	for _, l := range p.extra {
		l.stopAccepting()
	}
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
		_ = kill(p.cmd.Process, p.scanned, p.stopped)
	}
}

// kill marks castor's own kill only while stderr is open: an exited, unreaped process still accepts one.
func kill(proc *os.Process, exited <-chan struct{}, stopped *atomic.Bool) error {
	select {
	case <-exited:
		return os.ErrProcessDone
	default:
	}
	// Set first, so a Wait reaping what this kill ended already reads it as castor's.
	stopped.Store(true)
	err := proc.Kill()
	if err != nil {
		stopped.Store(false)
	}
	return err
}

// exitStatus reads the reaped process status; only castor's own kill leaves none.
func (p *Process) exitStatus() int {
	state := p.cmd.ProcessState
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		// Windows has no signals: its kill is exit code 1, told from a failure only by castor having sent it.
		if runtime.GOOS == "windows" && p.stopped.Load() {
			return noExitStatus
		}
		return state.ExitCode()
	}
	// Teardown kills before it waits, so a crash already reaped still names its own signal.
	if ws.Signal() == syscall.SIGKILL && p.stopped.Load() {
		return noExitStatus
	}
	return signalExitBase + int(ws.Signal())
}
