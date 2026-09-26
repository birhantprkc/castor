package execute

import (
	"context"
	"errors"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// outcome folds cast result and judgement into one value; read error travels beside cast error.
func (c *cast) outcome(ctx context.Context, err error) attempt.Outcome {
	e := c.evidence

	if c.reader != nil {
		// A read the cast's own teardown cancelled did not fail on its own account.
		if err := c.readErr(); !errors.Is(err, context.Canceled) {
			e.ReadErr = err
		}
		e.ReadExit = c.reader.ExitStatus()
		e.ReadIncomplete = c.reader.LostMedia()
		e.Copied = c.reader.Copying()
		e.Lines = c.reader.Evidence()
	}
	e.Cancelled = ctx.Err() != nil

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

	return attempt.Outcome{Err: err, Evidence: e}
}
