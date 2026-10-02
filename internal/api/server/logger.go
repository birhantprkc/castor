package server

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/wire"
)

// logger lets anyone follow a cast; following never drives it.
type logger struct{ registry *registry }

// Watch ends with the cast's outcome as its status: OK ended, canceled stopped, aborted failed with why.
func (l logger) Watch(ctx context.Context, req *castorv1.WatchRequest, out *connect.ServerStream[castorv1.WatchResponse]) error {
	s, err := l.registry.find(req.GetCastId())
	if err != nil {
		return err
	}
	var level slog.Level
	if req.Logs != nil {
		level = wire.FromLogLevel(*req.Logs)
	}
	outcome, err := s.watch(ctx, level, req.Logs != nil, out.Send)
	switch {
	case err != nil:
		return err
	case outcome == nil:
		return nil
	case errors.Is(outcome, errStopped):
		return connect.NewError(connect.CodeCanceled, outcome)
	}
	return connect.NewError(connect.CodeAborted, outcome)
}
