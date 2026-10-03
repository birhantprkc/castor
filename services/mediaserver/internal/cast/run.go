package cast

import (
	"context"
	"errors"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/services/mediaserver/internal/lend"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// run finds the source's streams, readies them and casts them on the lent device, then ends with how that went.
func (c *cast) run(caps media.Capabilities) {
	c.end(eventEnd, outcome(c.ctx, c.play(caps)))
}

func (c *cast) play(caps media.Capabilities) error {
	streams, err := c.source.streams(c.ctx, c.extractor)
	if err != nil {
		return err
	}
	c.fire(eventMeasure, func(next *view) { next.status.Streams = uint32(len(streams)) })
	ready, err := c.source.ready(c.ctx, c.caster, streams)
	if err != nil {
		return err
	}
	c.fire(eventRank, func(next *view) { next.status.Castable = uint32(len(ready)) })
	device := lend.NewDevice(c.line, caps, c.deliveries.Reached, func() { c.cancel(lend.ErrLenderLeft) })
	return c.caster.Play(c.ctx, device, c.deliveries, ready, c)
}

// outcome is how a cast that returned err ended: why its context ended if it did, stopped when nobody said why.
func outcome(ctx context.Context, err error) error {
	switch cause := context.Cause(ctx); {
	case cause == nil:
		return err
	case errors.Is(cause, context.Canceled):
		return errStopped
	default:
		return cause
	}
}

// Attempting shows the cast on its try-th attempt.
func (c *cast) Attempting(try int) {
	c.fire(eventAttempt, func(next *view) { next.status.Attempt = uint32(try) })
}

// Revising shows the strategy the cast is revising its attempt with, and why.
func (c *cast) Revising(strategy, why string) {
	c.fire(eventRevise, func(next *view) {
		next.status.Revision = &castorv1.Revision{Strategy: strategy, Why: why}
	})
}
