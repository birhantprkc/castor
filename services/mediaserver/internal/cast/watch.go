package cast

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// Watch follows a cast; following never drives it.
func (s *Service) Watch(ctx context.Context, req *castorv1.WatchRequest, out *connect.ServerStream[castorv1.WatchResponse]) error {
	c, err := s.find(req.GetCastId())
	if err != nil {
		return err
	}
	return c.watch(ctx, req.Logs, out.Send)
}

// watch sends the status at once and on every change, live log lines from logs on (nil asks none), and how the cast ended last; it never drives.
func (c *cast) watch(ctx context.Context, logs *castorv1.LogLevel, send func(*castorv1.WatchResponse) error) error {
	lines := c.logs.Subscribe(logs)
	defer c.logs.Unsubscribe(lines)
	var sent *castorv1.CastStatus
	for {
		now, changed := c.now.Load()
		if !proto.Equal(now.status, sent) {
			if err := send(&castorv1.WatchResponse{Update: &castorv1.WatchResponse_Status{Status: now.status}}); err != nil {
				return err
			}
			sent = now.status
		}
		if now.ended != nil {
			// Lines queued before the end were logged before it, so they go out before it does.
			for len(lines) > 0 {
				if err := send(<-lines); err != nil {
					return err
				}
			}
			return send(&castorv1.WatchResponse{Update: &castorv1.WatchResponse_Ended{Ended: now.ended}})
		}
		select {
		case <-changed:
		case line := <-lines:
			if err := send(line); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
