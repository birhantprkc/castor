package execute

import (
	"context"
	"errors"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// evidence is what an attempt left behind, read from how far it ran and how it ended.
func evidence(r ran, err error, cancelled bool) attempt.Evidence {
	e := attempt.Evidence{Reached: r.reached, Cancelled: cancelled}
	if err == nil {
		e.Reached = attempt.PhaseDelivered
	}
	// A read cast reaches its delivery only once the playback gate proved the buffer.
	e.Buffered = r.reader != nil && r.reached >= attempt.PhaseOpening

	if p := r.reader; p != nil {
		// A read the cast's own release cancelled did not fail on its own account.
		if err := p.Err(); !errors.Is(err, context.Canceled) {
			e.ReadErr = err
		}
		e.ReadExit = p.exitStatus()
		e.ReadIncomplete = p.lostMedia()
		e.Copied = p.copying()
		e.Lines = p.Evidence()
	}

	if refused, ok := errors.AsType[*playRefused](err); ok {
		e.PlayErr, e.Handoff = refused.err, refused.source
	}
	if unread, ok := errors.AsType[*timelineUnreadable](err); ok {
		e.TimelineErr = unread.err
	}
	if undelivered, ok := errors.AsType[*watch.Undelivered](err); ok {
		e.Undelivered = undelivered
	}
	// A renderer its family saw go away while the cast played (see supervising).
	if gone, ok := errors.AsType[*media.Gone](err); ok {
		e.RendererGone = gone
	}
	if verdict, ok := errors.AsType[*watch.Fault](err); ok {
		e.Verdict, e.Health = verdict.Kind, verdict.Health
		if len(e.Lines) == 0 {
			e.Lines = verdict.Evidence
		}
	}
	return e
}
