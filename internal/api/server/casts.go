package server

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"time"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// casts starts and stops casts; what one casts on is lent through the device service.
type casts struct {
	ctx      context.Context
	caster   func(asked *castorv1.Preferences) Caster
	server   *url.URL
	registry *registry
}

func (c *casts) StartCast(_ context.Context, req *castorv1.StartCastRequest) (*castorv1.StartCastResponse, error) {
	caster := c.caster(req.GetPreferences())
	ctx, cancel := context.WithCancelCause(c.ctx)
	s := newSession(ctx, rand.Text(), cancel, c.server)
	ctx = s.ctx
	c.registry.add(s)

	go func() {
		defer cancel(nil)
		// Nothing starts before a device is lent; a watcher opened first then misses nothing.
		waiting := time.NewTimer(undriven)
		select {
		case <-s.line.attached:
			waiting.Stop()
			s.end(c.run(ctx, s, caster, req))
		case <-waiting.C:
			s.end(errUndriven)
		case <-ctx.Done():
			s.end(outcome(ctx, nil))
		}
		c.registry.retire(s)
	}()
	return &castorv1.StartCastResponse{CastId: s.id}, nil
}

// run measures streams and casts them on the lent device, returning the outcome: nil ended, errStopped stopped, else why it failed.
func (c *casts) run(ctx context.Context, s *session, caster Caster, req *castorv1.StartCastRequest) error {
	s.status.update(func(now *castorv1.CastStatus) {
		now.Streams = uint32(handed(req))
	})
	ready, err := ready(ctx, caster, req)
	if err == nil {
		s.status.update(func(now *castorv1.CastStatus) { now.Castable = uint32(len(ready)) })
		err = caster.Play(ctx, remoteRenderer{s: s}, s.deliveries, ready, s)
	}
	return outcome(ctx, err)
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

func (c *casts) StopCast(_ context.Context, req *castorv1.StopCastRequest) (*castorv1.StopCastResponse, error) {
	s, err := c.registry.find(req.GetCastId())
	if err != nil {
		return nil, err
	}
	s.cancel(errStopped)
	return &castorv1.StopCastResponse{}, nil
}
