package server

import (
	"context"
	"log/slog"
	"sync"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/api/wire"
)

// logBuffer is how many lines a watcher may fall behind before the oldest unsent are dropped.
const logBuffer = 256

// logs sends a cast's log lines, live, to the watchers that asked for them; it keeps none.
type logs struct {
	mu       sync.Mutex
	watchers map[chan *castorv1.WatchResponse]slog.Level
}

// subscribe opens a line feed at level, or none (a nil feed) when the watcher asked for none.
func (l *logs) subscribe(level slog.Level, ok bool) chan *castorv1.WatchResponse {
	if !ok {
		return nil
	}
	lines := make(chan *castorv1.WatchResponse, logBuffer)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.watchers[lines] = level
	return lines
}

func (l *logs) unsubscribe(lines chan *castorv1.WatchResponse) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.watchers, lines)
}

func (l *logs) wants(level slog.Level) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, least := range l.watchers {
		if level >= least {
			return true
		}
	}
	return false
}

// publish never waits on a watcher: one that is behind loses the line, the cast does not stall.
func (l *logs) publish(r slog.Record, attrs []slog.Attr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var line *castorv1.WatchResponse
	for lines, least := range l.watchers {
		if r.Level < least {
			continue
		}
		if line == nil {
			line = &castorv1.WatchResponse{Update: &castorv1.WatchResponse_Line{Line: wire.Log(r, attrs)}}
		}
		select {
		case lines <- line:
		default:
		}
	}
}

type (
	castKey   struct{}
	servedKey struct{}
)

func castOf(ctx context.Context) (*session, bool) {
	s, ok := ctx.Value(castKey{}).(*session)
	return s, ok
}

func served(ctx context.Context) bool {
	_, ok := castOf(ctx)
	return ok || ctx.Value(servedKey{}) != nil
}

// Logs routes log records by where they come from: the server's go to server, a cast's also to its watchers, the rest to process.
func Logs(process, server slog.Handler) slog.Handler {
	return router{process: process, server: server}
}

type router struct {
	process, server slog.Handler
	attrs           []slog.Attr
}

func (h router) Enabled(ctx context.Context, level slog.Level) bool {
	if s, ok := castOf(ctx); ok && s.logs.wants(level) {
		return true
	}
	if served(ctx) {
		return h.server.Enabled(ctx, level)
	}
	return h.process.Enabled(ctx, level)
}

func (h router) Handle(ctx context.Context, r slog.Record) error {
	if s, ok := castOf(ctx); ok {
		s.logs.publish(r, h.attrs)
	}
	if !served(ctx) {
		return h.process.Handle(ctx, r)
	}
	if h.server.Enabled(ctx, r.Level) {
		return h.server.Handle(ctx, r)
	}
	return nil
}

func (h router) WithAttrs(attrs []slog.Attr) slog.Handler {
	return router{process: h.process.WithAttrs(attrs), server: h.server.WithAttrs(attrs), attrs: append(h.attrs[:len(h.attrs):len(h.attrs)], attrs...)}
}

// WithGroup qualifies only the local outputs: a watcher reads lines flat, keyed as logged.
func (h router) WithGroup(name string) slog.Handler {
	return router{process: h.process.WithGroup(name), server: h.server.WithGroup(name), attrs: h.attrs}
}
