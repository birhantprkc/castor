package pipeline

import (
	"context"

	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/deliver/spool"
	"github.com/stupside/castor/internal/cast/watch"
)

// watchTheRead judges one cast in one window over the read behind it. Everything that is a
// fact about the READ rather than about the window is stated here and nowhere else: who is
// producing, what it has landed, and the pace it may be judged against.
//
// Overwriting whatever the caller put in those three fields is the point, and it is what
// makes one misattribution unreachable rather than merely fixed. The pace is withheld from a
// read castor itself throttles or encodes (see pull.judgedPace), and a window that instead
// took the pace the POLICY granted judged a whisper cast or a floor encode against 2x: at
// the measured 0.0627x that abandons the cast mid-title, to a viewer who is watching, with a
// message naming a source link that was never the bottleneck, and no cast someone is
// watching is started over. A window states which side of the gate it is on and what the
// renderer's side can say; it states nothing about the read, so no two windows can disagree
// about it.
func watchTheRead(ctx context.Context, sp *spool.Spool, pl *pull, m watch.Monitor) error {
	m.Producer = pl
	m.Telemetry = pl
	m.Landed = sp.Size
	m.Headroom = pl.judgedPace()
	return watch.Watch(ctx, m)
}

// waitForPlayable holds the cast until the encoder may start reading the spool: the
// pull has landed media, that read has proved it can deliver at the pace it may be held
// to (see watchTheRead), and either whisper has built up its lead (or finished, on short sources) or
// this cast burns no subtitles. It ends early with a fault when the read dies, stalls,
// or is measured as slower than playback.
func waitForPlayable(ctx context.Context, tr watch.Lead, sp *spool.Spool, pl *pull) error {
	return watchTheRead(ctx, sp, pl, watch.Monitor{
		Subject: "playback gate",
		Window:  watch.BeforePlay,
		Lead:    tr,
	})
}

// supervise judges the cast while the renderer is playing it, which nothing did before:
// the executor touched the reader for the last time on its way to Play, so a read that
// starved after playback began ran to the end of the title, and a renderer that took
// the URL and fetched nothing was reported as a delivered cast.
//
// It watches the same read through the same rules as the gate above, on the same terms
// (see watchTheRead), and additionally what the delivery can say about the renderer: that it
// is fetching, which is the only evidence castor has that anybody is watching, and how much
// is left for it to fetch, which is what keeps the silence of a viewer who paused from being
// read as a renderer that went away. Nothing here can revise the attempt: a viewer is
// already watching, so the action table answers this window with attribution instead (see
// watch.actions).
//
// The delivery arrives as one value carrying both facts, which is why this cannot be handed
// half of them: a supervisor given the fetching without the buffer ends a film at two and a
// half minutes of somebody standing up.
//
// It is what THIS leg supervises with, because this leg opened a read of its own. A leg whose
// encode is the read supplies none and the delivery driver judges that encode instead, so
// whether a cast is watched at all is no longer something a leg can decide by omission (see
// core.Supervisor).
func supervise(ctx context.Context, sp *spool.Spool, pl *pull, d core.Delivery) error {
	return watchTheRead(ctx, sp, pl, watch.Monitor{
		Subject:   "the playing cast",
		Window:    watch.Playing,
		Consumer:  d.Consumer,
		Delivered: d.Delivered,
	})
}
