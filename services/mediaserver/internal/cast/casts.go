package cast

import (
	"context"
	"crypto/rand"
	"net/url"

	"connectrpc.com/connect"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// casts starts, stops and follows casts; what one casts on is lent through the device service.
type casts struct {
	ctx       context.Context
	extractor Extractor
	caster    func(asked *castorv1.Preferences) Caster
	reach     *url.URL
	registry  *registry
}

func (c *casts) Start(_ context.Context, req *mediav1.StartRequest) (*mediav1.StartResponse, error) {
	s := newSession(c.ctx, rand.Text(), c.reach, c.extractor, c.caster(req.GetPreferences()), req.GetSource())
	if err := c.registry.add(s); err != nil {
		s.cancel(errShutdown)
		return nil, err
	}
	return &mediav1.StartResponse{CastId: s.id}, nil
}

func (c *casts) Stop(_ context.Context, req *mediav1.StopRequest) (*mediav1.StopResponse, error) {
	s, err := c.registry.find(req.GetCastId())
	if err != nil {
		return nil, err
	}
	s.cancel(errStopped)
	return &mediav1.StopResponse{}, nil
}

// Watch follows a cast; following never drives it.
func (c *casts) Watch(ctx context.Context, req *castorv1.WatchRequest, out *connect.ServerStream[castorv1.WatchResponse]) error {
	s, err := c.registry.find(req.GetCastId())
	if err != nil {
		return err
	}
	return s.watch(ctx, req.Logs, out.Send)
}
