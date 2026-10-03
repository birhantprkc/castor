// Package castlog routes the media server's log records: a cast's lines live to the watchers that asked for them, the server's to its own output.
package castlog

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
)

type (
	feedKey   struct{}
	servedKey struct{}
)

// Into carries f in ctx, so every line logged under it reaches f's watchers.
func Into(ctx context.Context, f *Feed) context.Context {
	return context.WithValue(ctx, feedKey{}, f)
}

// Served marks what h logs while answering a request as the server's, apart from the rest of its process.
func Served(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), servedKey{}, true)))
	})
}

func feedOf(ctx context.Context) (*Feed, bool) {
	f, ok := ctx.Value(feedKey{}).(*Feed)
	return f, ok
}

func served(ctx context.Context) bool {
	_, ok := feedOf(ctx)
	return ok || ctx.Value(servedKey{}) != nil
}

// Router routes records by where they come from: the server's go to server, a cast's also to its watchers, the rest to process.
func Router(process, server slog.Handler) slog.Handler {
	return slog.NewMultiHandler(watchers{}, side{process: process, server: server})
}

// watchers hands a cast's records to its feed.
type watchers struct{ attrs []slog.Attr }

func (h watchers) Enabled(ctx context.Context, level slog.Level) bool {
	f, ok := feedOf(ctx)
	return ok && f.wants(level)
}

func (h watchers) Handle(ctx context.Context, r slog.Record) error {
	if f, ok := feedOf(ctx); ok {
		f.publish(r, h.attrs)
	}
	return nil
}

func (h watchers) WithAttrs(attrs []slog.Attr) slog.Handler {
	return watchers{attrs: slices.Concat(h.attrs, attrs)}
}

// WithGroup leaves the lines flat: a watcher reads them keyed as logged.
func (h watchers) WithGroup(string) slog.Handler { return h }

// side is the local output a record belongs to: the server's, or the process's.
type side struct{ process, server slog.Handler }

func (h side) of(ctx context.Context) slog.Handler {
	if served(ctx) {
		return h.server
	}
	return h.process
}

func (h side) Enabled(ctx context.Context, level slog.Level) bool {
	return h.of(ctx).Enabled(ctx, level)
}

func (h side) Handle(ctx context.Context, r slog.Record) error { return h.of(ctx).Handle(ctx, r) }

func (h side) WithAttrs(attrs []slog.Attr) slog.Handler {
	return side{process: h.process.WithAttrs(attrs), server: h.server.WithAttrs(attrs)}
}

func (h side) WithGroup(name string) slog.Handler {
	return side{process: h.process.WithGroup(name), server: h.server.WithGroup(name)}
}
