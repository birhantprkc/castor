// Package server is castor's engine behind the cast contract: it reads and serves casts, and drives renderers only through its clients.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	"connectrpc.com/validate"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
	"github.com/stupside/castor/internal/cast/attempt"
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
	// Play casts streams on renderer, serving only on listeners, which the client relays.
	Play(ctx context.Context, renderer execute.Renderer, listeners execute.Listeners, streams []*source.Stream, turns attempt.Turns) error
}

// handler serves the cast services, the relay, health and reflection; casts live until ctx ends.
func handler(ctx context.Context, b Backend) http.Handler {
	reg := newRegistry()
	// Requests are held to the rules the contract states, before any handler reads them.
	valid := connect.WithInterceptors(validate.NewInterceptor())
	mux := http.NewServeMux()
	mux.Handle(castorv1connect.NewCastServiceHandler(&casts{ctx: ctx, caster: b.Caster, undriven: undriven, registry: reg}, valid))
	mux.Handle(castorv1connect.NewDeviceServiceHandler(devices{registry: reg}, valid))
	mux.Handle(castorv1connect.NewLoggerServiceHandler(logger{registry: reg}, valid))
	mux.Handle(castorv1connect.NewStreamServiceHandler(ranking{caster: b.Caster}, valid))
	mux.Handle(castorv1connect.NewExtractServiceHandler(extraction{b.Extractor}, valid))
	mux.Handle(relayRoute, relay{registry: reg})
	services := []string{
		castorv1connect.CastServiceName, castorv1connect.DeviceServiceName,
		castorv1connect.LoggerServiceName, castorv1connect.StreamServiceName,
		castorv1connect.ExtractServiceName,
	}
	mux.Handle(grpchealth.NewHandler(grpchealth.NewStaticChecker(services...)))
	reflector := grpcreflect.NewStaticReflector(services...)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	return mux
}

// shutdownGrace is how long in-flight requests get to finish once ctx ends.
const shutdownGrace = 5 * time.Second

// Serve answers on l until ctx ends.
func Serve(ctx context.Context, l net.Listener, b Backend) error {
	slog.InfoContext(ctx, "api serving", "address", l.Addr().String())
	return serve(ctx, l, b)
}

func serve(ctx context.Context, l net.Listener, b Backend) error {
	// Cleartext HTTP/2 beside HTTP/1.1, so gRPC tools reach reflection and health on the same port.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:           handler(ctx, b),
		ReadHeaderTimeout: 10 * time.Second,
		Protocols:         &protocols,
		// Every request is marked the server's, so what it logs is told apart from its client's in one process.
		BaseContext: func(net.Listener) context.Context { return context.WithValue(context.Background(), servedKey{}, true) },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("api server: %w", err)
	}
	return nil
}

// Embedded serves b in this process on loopback and returns its base URL.
func Embedded(ctx context.Context, b Backend) (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("embedded api: %w", err)
	}
	go func() {
		if err := serve(ctx, l, b); err != nil {
			slog.ErrorContext(ctx, "embedded api stopped", "error", err)
		}
	}()
	return "http://" + l.Addr().String(), nil
}
