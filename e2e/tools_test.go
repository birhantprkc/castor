package e2e

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// tools are the programs one cell drives. They are resolved once per run and
// passed by value, so what a cell depends on is visible in its signature rather
// than reached for from package state.
var findTools = sync.OnceValues(func() (tools, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return tools{}, err
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return tools{}, err
	}
	return tools{ffmpeg: ffmpeg, ffprobe: ffprobe}, nil
})

type tools struct {
	ffmpeg  string
	ffprobe string
}

// newTools resolves the media tools, skipping the cell when one is absent: a
// missing ffmpeg is an environment this suite cannot run in, not a failure of the
// code it covers.
func newTools(t *testing.T) tools {
	t.Helper()
	if testing.Short() {
		t.Skip("live streams run in real time; skipped in -short mode")
	}

	found, err := findTools()
	if err != nil {
		t.Skipf("%v; this suite drives real ffmpeg", err)
	}
	return found
}

// errWaitedTooLong is what await reports when a condition never came true. It is
// a sentinel so a caller can tell "not yet" from "the run died".
var errWaitedTooLong = errors.New("condition never held")

// await blocks until cond holds, the context ends, or within elapses. It exists
// so no case hand-rolls another ticker-and-deadline select, and so the reason a
// wait ended is a value the caller can report with its own context attached.
func await(ctx context.Context, within time.Duration, cond func() bool) error {
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for !cond() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errWaitedTooLong
		case <-tick.C:
		}
	}
	return nil
}
