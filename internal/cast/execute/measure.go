package execute

import (
	"context"
	"log/slog"

	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/media"
)

// facts is what a cast knows about its tracks and whether it knows the whole subject.
type facts struct {
	Probe    media.ProbeInfo
	Measured bool
}

// measure probes subject once; failure means fallback to re-encode (zero ProbeInfo is copy-safe).
func measure(ctx context.Context, subject string, p media.Prober) facts {
	ctx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()

	// Reach separation (refused vs. unreached) costs the same re-encode by cast time.
	info, _, err := p.Probe(ctx)
	if err != nil {
		slog.WarnContext(ctx, "castor could not measure this cast's tracks; every copy decision below falls back to a re-encode, and the read is where a dead link will say so",
			"subject", subject, "error", err)
	}
	return facts{Probe: info, Measured: err == nil}
}

// probeBudget bounds probe time; ffmpeg HLS demuxer walks whole 403-playlist (199s seen).
const probeBudget = read.BackoffMax / 2
