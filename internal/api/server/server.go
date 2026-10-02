// Package server is castor's engine behind the cast contract: it reads and serves casts, and drives renderers only through its clients.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	"connectrpc.com/validate"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/source"
)

// Backend is what the server casts with, bound at the composition root.
type Backend struct {
	Extractor Extractor
	// Caster binds one cast to what its client asked of it.
	Caster func(asked *castorv1.Preferences) Caster
}

// Extractor finds the streams pages play.
type Extractor interface {
	ExtractAll(ctx context.Context, pages []string) ([]*source.Stream, error)
}

// Caster readies and plays one cast's streams: found streams are ranked, a named one measured (and identified when it says nothing).
type Caster interface {
	Rank(ctx context.Context, streams []*source.Stream) ([]*source.Stream, error)
	Measure(ctx context.Context, stream *source.Stream) (*source.Stream, error)
	Play(ctx context.Context, renderer execute.Renderer, listeners deliver.Listeners, streams []*source.Stream, turns attempt.Turns) error
}

// handlers are the API and the media route renderers reach at server.
func handlers(ctx context.Context, b Backend, server *url.URL) (api, renderers http.Handler) {
	reg := newRegistry()
	// Both ways, every message is held to the rules the contract states.
	valid := connect.WithInterceptors(validate.NewInterceptor(validate.WithValidateResponses()))
	mux := http.NewServeMux()
	mux.Handle(castorv1connect.NewCastServiceHandler(&casts{ctx: ctx, caster: b.Caster, undriven: undriven, server: server, registry: reg}, valid))
	mux.Handle(castorv1connect.NewDeviceServiceHandler(devices{registry: reg}, valid))
	mux.Handle(castorv1connect.NewLoggerServiceHandler(logger{registry: reg}, valid))
	mux.Handle(castorv1connect.NewStreamServiceHandler(ranking{caster: b.Caster}, valid))
	mux.Handle(castorv1connect.NewExtractServiceHandler(extraction{b.Extractor}, valid))
	services := []string{
		castorv1connect.CastServiceName, castorv1connect.DeviceServiceName,
		castorv1connect.LoggerServiceName, castorv1connect.StreamServiceName,
		castorv1connect.ExtractServiceName,
	}
	mux.Handle(grpchealth.NewHandler(grpchealth.NewStaticChecker(services...)))
	reflector := grpcreflect.NewStaticReflector(services...)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	media := http.NewServeMux()
	media.Handle(mediaPattern, newMediaRoute(reg))
	return mux, media
}

// shutdownGrace is how long in-flight requests get to finish once ctx ends.
const shutdownGrace = 5 * time.Second

// Serve answers the API and renderers on l until ctx ends; renderers are told to reach it at server.
func Serve(ctx context.Context, l net.Listener, server *url.URL, b Backend) error {
	slog.InfoContext(ctx, "serving", "address", l.Addr().String(), "renderers_reach", server.String())
	api, renderers := handlers(ctx, b, server)
	mux := http.NewServeMux()
	mux.Handle("/", api)
	mux.Handle(mediaPattern, renderers)
	return serve(ctx, l, mux)
}

func serve(ctx context.Context, l net.Listener, h http.Handler) error {
	// Cleartext HTTP/2 beside HTTP/1.1, so gRPC tools reach reflection and health on the same port.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		Protocols:         &protocols,
		// Every request is marked the server's, so what it logs is told apart from its client's in one process.
		BaseContext: func(net.Listener) context.Context { return context.WithValue(context.Background(), servedKey{}, true) },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	select {
	case err := <-served:
		return fmt.Errorf("castor server: %w", err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		// The grace ran out: what still runs is cut.
		return srv.Close()
	}
	return nil
}

// Embedded keeps the API on loopback, for this process alone, and serves renderers on lan; it returns the API's URL.
func Embedded(ctx context.Context, b Backend, lan net.Listener) (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("embedded api: %w", err)
	}
	api, renderers := handlers(ctx, b, &url.URL{Scheme: "http", Host: lan.Addr().String()})
	go embed(ctx, l, api)
	go embed(ctx, lan, renderers)
	return "http://" + l.Addr().String(), nil
}

func embed(ctx context.Context, l net.Listener, h http.Handler) {
	if err := serve(ctx, l, h); err != nil {
		slog.ErrorContext(ctx, "embedded api stopped", "error", err)
	}
}
