package core

import (
	"context"
	"log/slog"

	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// Prober measures the tracks a reader will actually map. It is bound to its subject
// rather than taking one, so a probe of a hostile network source and a probe of the
// local buffer are the same port under the same budget: the thing worth stating once is
// not the subprocess, it is what a cast does when the measurement does not arrive.
type Prober interface {
	Probe(ctx context.Context) (media.ProbeInfo, error)
}

// Facts is what a cast knows about the tracks it is about to handle, and whether it
// knows anything at all.
//
// Probe is whatever answered, which on a partial failure is the half that did: a demuxed
// program whose audio rendition could not be read still measured its video, and the video
// axis is still decided from it (the audio half is zeroed at the source, because it would
// otherwise describe input 0 while the encode maps input 1).
//
// Measured is whether the WHOLE subject answered, so it is false for that partial case
// too. It travels beside the probe because "castor never measured this" is a fact a
// diagnosis reads rather than a warning that was logged: a cast that dies against a
// source nothing ever measured was unreachable, not a copy that broke.
type Facts struct {
	Probe    media.ProbeInfo
	Measured bool
}

// Measure drives exactly one probe of subject and owns the rule each of its callers used
// to restate in its own prose: a probe that fails means NOTHING IS KNOWN against the
// subject, and every copy decision below falls back accordingly. That fallback is already
// the shape of the data (media.ProbeInfo's zero value is copy-safe on no axis, and an
// unmeasured codec matches no carriage rule and no copy adaptation), so the failure costs
// a re-encode and never a cast.
//
// The failure is reported and not swallowed. A source probe opens the upstream on exactly
// the terms the read that follows will, so its failure is the earliest evidence a cast has
// that the link is dead, and it arrives before a single byte has been asked for. Swallowing
// it meant a source whose segments all 403 was discovered here, in silence, and then
// discovered all over again by the read.
func Measure(ctx context.Context, subject string, p Prober) Facts {
	ctx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()

	info, err := p.Probe(ctx)
	if err != nil {
		slog.WarnContext(ctx, "castor could not measure this cast's tracks; every copy decision below falls back to a re-encode, and the read is where a dead link will say so",
			"subject", subject, "error", err)
	}
	return Facts{Probe: info, Measured: err == nil}
}

// probeBudget is how long one measurement may take before the cast proceeds on nothing
// known. It is derived from the reader's own reconnect ceiling and not configured: a
// measurement that outlives half a reconnect cycle costs more than the read it is
// protecting, since the reader it informs is allowed to spend a whole such cycle waiting
// out a rate limiter before the retry that lands (see read.BackoffMax).
//
// It is not a nicety, because there is a shape of dead source that makes an unbounded
// probe outlive the cast's patience entirely: ffmpeg's HLS demuxer answers a playlist
// whose segments all 403 by walking the whole playlist, skipping each segment after it has
// "failed too many times", which for a feature title is thousands of round trips producing
// no output and no exit. Measured on a real one, that was 199 seconds of a run printing
// nothing at all. Since a failed measurement costs at worst a re-encode, the bound buys
// the cast the right to fail at the read instead, where the reason can be named.
const probeBudget = read.BackoffMax / 2
