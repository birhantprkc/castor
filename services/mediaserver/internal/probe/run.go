package probe

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

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
		"-show_entries", showEntries,
		// The first frames only: a demuxer in front of the decoder (HLS) leaves field order unknown until one decodes.
		"-read_intervals", "%+#2",
	}
	args = append(args, p.inputArgs...)
	args = append(args, p.input)

	var stdout bytes.Buffer
	said, err := ffmpeg.Run(ctx, p.ffprobePath, args, nil, &stdout)
	if err != nil {
		if ctx.Err() != nil {
			// Only the budget can have expired while the caller's clock still runs.
			if parent.Err() == nil {
				return media.ProbeInfo{}, media.ReachUnproven, fmt.Errorf("ffprobe: %w (measurement budget %s)%s", err, p.budget, evidence(said))
			}
			return media.ProbeInfo{}, media.ReachUnproven, fmt.Errorf("ffprobe hit castor's probe deadline: %w%s", parent.Err(), evidence(said))
		}
		return media.ProbeInfo{}, classifyReach(said), fmt.Errorf("ffprobe: %w%s", err, evidence(said))
	}
	info, err := decode(stdout.Bytes(), p.videoIndex, p.audioIndex)
	return info, media.ReachOpened, err
}

// evidence renders what ffprobe said as a suffix for the error, or nothing when it said nothing.
func evidence(said string) string {
	if said == "" {
		return ""
	}
	return "\n" + said
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
