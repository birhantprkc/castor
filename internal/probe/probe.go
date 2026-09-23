package probe

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/stupside/castor/internal/media"
)

const probeStderrTail = 8 << 10

// tailWriter keeps the last probeStderrTail bytes written to it.
type tailWriter struct{ buf []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if excess := len(w.buf) - probeStderrTail; excess > 0 {
		w.buf = append(w.buf[:0], w.buf[excess:]...)
	}
	return len(p), nil
}

func (w *tailWriter) said() string { return string(w.buf) }

// evidence renders what ffprobe said as a suffix for the error, or nothing when it said nothing.
func (w *tailWriter) evidence() string {
	if said := strings.TrimSpace(w.said()); said != "" {
		return "\n" + said
	}
	return ""
}

// pass is one ffprobe invocation: binary, clock, input, and which kind-relative tracks to read.
type pass struct {
	ffprobePath string
	budget      time.Duration
	inputArgs   []string
	input       string
	videoIndex  int
	audioIndex  int
}

// run opens the input, decodes the answer, and reports how far the origin let ffprobe get.
func (p pass) run(ctx context.Context) (media.ProbeInfo, media.Reach, error) {
	// The caller's own context is kept because the budget derived below hides it.
	parent := ctx
	if p.budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.budget)
		defer cancel()
	}

	args := []string{
		// Warning, not error: the HTTP status of a refused segment or a 410 is only said at warning.
		"-v", "warning",
		"-print_format", "json",
		"-show_entries", probeEntries,
	}
	args = append(args, p.inputArgs...)
	args = append(args, p.input)

	cmd := exec.CommandContext(ctx, p.ffprobePath, args...)
	var stdout bytes.Buffer
	tail := &tailWriter{}
	cmd.Stdout = &stdout
	cmd.Stderr = tail
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			// Only the budget can have expired while the caller's clock still runs.
			if parent.Err() == nil {
				return media.ProbeInfo{}, media.ReachUnproven, fmt.Errorf("ffprobe: %w (measurement budget %s)%s", err, p.budget, tail.evidence())
			}
			return media.ProbeInfo{}, media.ReachUnproven, fmt.Errorf("ffprobe hit castor's probe deadline: %w%s", parent.Err(), tail.evidence())
		}
		return media.ProbeInfo{}, classifyReach(tail.said()), fmt.Errorf("ffprobe: %w%s", err, tail.evidence())
	}
	info, err := decodeProbeTracks(stdout.Bytes(), p.videoIndex, p.audioIndex)
	return info, media.ReachOpened, err
}

// refusals are the origin answers no reader can talk past, matched on text ffmpeg's HTTP protocol writes.
var refusals = []string{"401 Unauthorized", "403 Forbidden", "404 Not Found", "410 Gone"}

// classifyReach reads how far the origin let ffprobe get from what ffprobe said on the way out.
func classifyReach(stderr string) media.Reach {
	if slices.ContainsFunc(refusals, func(status string) bool { return strings.Contains(stderr, status) }) {
		return media.ReachRefused
	}
	return media.ReachUnproven
}

// File binds a local path to this ffprobe; safe to point at a still-growing spool.
func (bin FFprobe) File(path string) media.Prober {
	return fileProber{ffprobePath: string(bin), path: path}
}

type fileProber struct{ ffprobePath, path string }

func (p fileProber) Probe(ctx context.Context) (media.ProbeInfo, media.Reach, error) {
	return pass{ffprobePath: p.ffprobePath, input: p.path}.run(ctx)
}
