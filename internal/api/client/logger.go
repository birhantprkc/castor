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

// Watch opens an observation of cast id and returns once its current status is shown, so a device lent after misses nothing.
// The server's lines for the cast go to logs at the least severe level it takes; nil asks for none.
func (c *Client) Watch(ctx context.Context, id string, progress Progress, logs slog.Handler) (*Watch, error) {
	level, logged := floor(ctx, logs)
	stream, err := c.logger.Watch(ctx, &castorv1.WatchRequest{CastId: id, Logs: wire.LogLevel(level, logged)})
	if err != nil {
		return nil, fmt.Errorf("watching cast: %w", err)
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
	switch {
	case w.ctx.Err() != nil:
		return context.Cause(w.ctx)
	case err == nil:
		return nil
	case connect.CodeOf(err) == connect.CodeCanceled:
		return ErrStopped
	}
	if failed, ok := errors.AsType[*connect.Error](err); ok && failed.Code() == connect.CodeAborted {
		return errors.New(failed.Message())
	}
	return fmt.Errorf("watching cast: %w", err)
}

// floor is the least severe level h takes, ok false when there is no h.
func floor(ctx context.Context, h slog.Handler) (slog.Level, bool) {
	if h == nil {
		return 0, false
	}
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn} {
		if h.Enabled(ctx, level) {
			return level, true
		}
	}
	return slog.LevelError, true
}
