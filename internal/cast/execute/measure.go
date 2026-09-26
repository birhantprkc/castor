package execute

import (
	"context"
	"log/slog"
	"time"

	"github.com/stupside/castor/internal/cast/read"
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

// sounds is whether program's read is sure to carry sound: heard by the probe, or else declared or required by the source.
func (f facts) sounds(p media.Program) bool {
	if f.Probe.AudioCodec != "" || f.Measured {
		return f.Probe.AudioCodec != ""
	}
	if known, _ := p.Measurement(); known.AudioCodec != "" {
		return true
	}
	track, ok := p.Track(media.TrackAudio)
	return ok && !track.Optional
}

// probeBudget bounds probe time; ffmpeg HLS demuxer walks whole 403-playlist (199s seen).
const probeBudget = read.BackoffMax / 2

// startSlack is under a frame at any real rate: two inputs this close already open together.
const startSlack = 10 * time.Millisecond

// aligned offsets each input by its measured start, since ffmpeg rebases every input to zero.
func aligned(p media.Program, starts map[media.InputID]time.Duration) media.Program {
	clock, measured := starts[p.ClockInput]
	if !measured || len(p.Inputs) < 2 {
		return p
	}
	out := p.Clone()
	for _, input := range p.Inputs {
		start, ok := starts[input.ID]
		_, declared := p.Offsets[input.ID]
		if !ok || declared || input.ID == p.ClockInput || (start-clock).Abs() < startSlack {
			continue
		}
		if out.Offsets == nil {
			out.Offsets = map[media.InputID]time.Duration{}
		}
		out.Offsets[input.ID] = start - clock
	}
	return out
}
