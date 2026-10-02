package server

import (
	"context"
	"crypto/rand"
	"net/url"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// casts starts and stops casts; what one casts on is lent through the device service.
type casts struct {
	ctx      context.Context
	caster   func(asked *castorv1.Preferences) Caster
	server   *url.URL
	registry *registry
}

func (c *casts) Start(_ context.Context, req *castorv1.StartRequest) (*castorv1.StartResponse, error) {
	ctx, cancel := context.WithCancelCause(c.ctx)
	s := newSession(ctx, rand.Text(), cancel, c.server, c.caster(req.GetPreferences()), req)
	c.registry.add(s)
	return &castorv1.StartResponse{CastId: s.id}, nil
}

func (c *casts) Stop(_ context.Context, req *castorv1.StopRequest) (*castorv1.StopResponse, error) {
	s, err := c.registry.find(req.GetCastId())
	if err != nil {
		return nil, err
	}
	s.cancel(errStopped)
	return &castorv1.StopResponse{}, nil
}
