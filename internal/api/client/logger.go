package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/wire"
)

// Progress is shown each status a watched cast reaches.
type Progress interface {
	Status(*castorv1.CastStatus)
}

// Watch is an open observation of one cast; it never drives the cast's renderer.
type Watch struct {
	ctx      context.Context
	stream   *connect.ServerStreamForClient[castorv1.WatchResponse]
	progress Progress
	logs     slog.Handler
}

// Watch observes cast id, its lines from level on going to logs (nil asks none), and returns once its status is shown.
func (c *Client) Watch(ctx context.Context, id string, progress Progress, logs slog.Handler, level slog.Level) (*Watch, error) {
	req := &castorv1.WatchRequest{CastId: id}
	if logs != nil {
		req.Logs = wire.LogLevel(level).Enum()
	}
	stream, err := c.logger.Watch(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("watching cast: %w", refused(err))
	}
	w := &Watch{ctx: ctx, stream: stream, progress: progress, logs: logs}
	if !w.next() {
		return nil, w.outcome()
	}
	return w, nil
}

// Outcome follows the cast to its end: nil when it ended, ErrStopped when it was stopped, why it failed otherwise.
func (w *Watch) Outcome() error {
	for w.next() {
	}
	return w.outcome()
}

func (w *Watch) next() bool {
	if !w.stream.Receive() {
		return false
	}
	switch u := w.stream.Msg().GetUpdate().(type) {
	case *castorv1.WatchResponse_Status:
		w.progress.Status(u.Status)
	case *castorv1.WatchResponse_Line:
		_ = w.logs.Handle(w.ctx, wire.FromLog(u.Line))
	}
	return true
}

// outcome reads the stream's own status, which is the cast's.
func (w *Watch) outcome() error {
	defer func() { _ = w.stream.Close() }()
	err := w.stream.Err()
	if w.ctx.Err() != nil {
		return context.Cause(w.ctx)
	}
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*connect.Error](err); ok {
		switch e.Code() {
		case connect.CodeCanceled:
			return ErrStopped
		case connect.CodeAborted:
			return errors.New(e.Message())
		}
	}
	return fmt.Errorf("watching cast: %w", refused(err))
}
