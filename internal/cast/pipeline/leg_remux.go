package pipeline

import (
	"context"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/ffmpeg"
)

// remux is the single-ffmpeg served cast for a renderer that fetches for itself and cannot be
// handed the source: network input, video copied where the muxer can carry it, container
// changed to the one the renderer asked for. No buffer of castor's own and no burn-in, since
// the encode IS the read.
//
// The delivery mechanism (a replay-from-zero stream or a live segmented directory) follows
// from the format the renderer declared and is chosen inside the delivery driver, not here.
//
// There is no reader of castor's own on this composition either, which is why the landing
// blames nobody but itself: the delivery's verdict on the encode is the whole account of what
// happened, and there is no second party whose error could be reported under the first one's
// name.
func remux(ctx context.Context, c *cast) landing {
	dev, err := c.renderer(ctx)
	if err != nil {
		return landing{err: err}
	}
	into, err := core.ServedFormat(dev.Capabilities())
	if err != nil {
		return landing{err: err}
	}

	// The upstream is measured before the encode is built, because every copy decision in it
	// (and every adaptation those copies need) is a function of the measurement. This
	// composition has no buffer, so it measures the upstream directly: one extra fetch before
	// the remux ffmpeg opens the same URL again. For the usual remux input (a static file in a
	// container the renderer will not take, say an AVI) that is fine. It is NOT free for a
	// short-lived or single-use signed link, where it can burn the token or a rate-limit slot
	// and leave the remux ffmpeg with a 403/429. An HLS source reaches here whenever it is
	// header-gated, which is the common signed-link shape, so this is a known and accepted
	// cost. It reads on the same terms the remux itself will, so it fails only where the remux
	// would.
	source := ffmpeg.NewNetworkSource(c.attempt.Source, c.attempt.Read)
	facts := core.Measure(ctx, "the source this remux reads", ffmpeg.SourceProbe(c.cfg.Transcode.FFprobePath, source))

	opts := c.encode(ctx, dev.Capabilities(), into, encodeInput{facts: facts, source: source})

	// This composition can fail with the renderer already fetching it just as the buffered one
	// can (the remux ffmpeg IS the read, so an expired signed link kills the thing being
	// delivered mid-title), so the phase comes from the delivery. What it reaches BEFORE Play is
	// nothing this leg established: there is no reader of castor's own here, and how far a remux
	// got on its way to an artifact is the artifact gate's verdict to report.
	//
	// It supervises with nothing, and that is the whole of what this leg has to say about it: the
	// nil is not "leave this cast unwatched" but "there is no read of mine to watch", so the
	// delivery driver judges the encode it started, which here IS the read (see core.Supervisor).
	// While this was a field on the params rather than an argument, omitting it left every cast of
	// a header-gated source to a self-fetching renderer with no in-flight judgement at all.
	reached, err := c.serve(ctx, dev, attempt.PhaseUnstarted, core.OpenParams{Opts: opts}, nil)
	if err != nil {
		return landing{err: err, Evidence: attempt.Evidence{Reached: reached}}
	}
	return landing{Evidence: attempt.Evidence{Reached: attempt.PhaseDelivered}}
}
