package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
)

// stderrTailCapacity bounds retained lines: startup burst (~20) plus two minutes of progress.
const stderrTailCapacity = 128

type Evidence struct {
	// ExitStatus is the process exit code (noExitStatus = not reaped or castor killed it).
	ExitStatus int

	// Lines are the most recent stderr lines, kept even while the process runs.
	Lines []string
}

// Evidence collects what the process left behind (read after Wait for complete status).
func (p *Process) Evidence() Evidence {
	return Evidence{
		ExitStatus: int(p.status.Load()),
		Lines:      p.tail.snapshot(),
	}
}

func (p *Process) LogStderrTail(ctx context.Context, msg string) {
	for _, line := range p.tail.snapshot() {
		slog.WarnContext(ctx, msg, "line", line)
	}
}

// stderrLineBuffer bounds one line read; Scanner would abandon over-long lines (blocks ffmpeg).
const stderrLineBuffer = 16 << 10

// drainStderr reads stderr line by line into the tail and the marker watch; it must read to EOF.
func drainStderr(ctx context.Context, r io.Reader, tail *ringTail, markers *markerWatch) {
	buffered := bufio.NewReaderSize(r, stderrLineBuffer)
	for {
		chunk, err := buffered.ReadSlice('\n')
		if line := strings.TrimRight(string(chunk), "\r\n"); line != "" {
			tail.Observe(line)
			markers.Observe(line)
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

// ringTail keeps the most recent N stderr lines (marker watch is separate; needs non-tail lines).
type ringTail struct {
	mu  sync.Mutex
	buf []string
}

func newRingTail() *ringTail {
	return &ringTail{buf: make([]string, 0, stderrTailCapacity)}
}

func (t *ringTail) Observe(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) == stderrTailCapacity {
		t.buf = t.buf[1:]
	}
	t.buf = append(t.buf, line)
}

func (t *ringTail) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.buf)
}
