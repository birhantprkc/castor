// Package transport is how castor's processes talk over HTTP: serving with a graceful end, a bearer token on both sides.
package transport

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	"connectrpc.com/validate"
)

// shutdownGrace is how long requests and casts get to finish once a server is told to stop.
const shutdownGrace = 10 * time.Second

// Serve answers h on l until ctx ends, then closes l and runs drain beside the requests still finishing, within shutdownGrace.
func Serve(ctx context.Context, l net.Listener, h http.Handler, drain func(context.Context)) error {
	// Cleartext HTTP/2 beside HTTP/1.1, so gRPC tools reach reflection and health on the same port.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, Protocols: &protocols}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	select {
	case err := <-served:
		return fmt.Errorf("serving %s: %w", l.Addr(), err)
	case <-ctx.Done():
	}
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	var draining sync.WaitGroup
	draining.Go(func() { drain(grace) })
	err := srv.Shutdown(grace)
	draining.Wait()
	if err != nil {
		// The grace ran out: what still runs is cut.
		return srv.Close()
	}
	return nil
}

// Endpoint is where a server answers, and the token it asks for.
type Endpoint struct {
	URL   string
	Token string
}

// Client is a client of e, carrying its token.
func (e Endpoint) Client() *http.Client { return Bearer(e.Token) }

// Background serves h on l until stop, outliving ctx's cancellation so what it runs can still wind down.
func Background(ctx context.Context, l net.Listener, h http.Handler, drain func(context.Context)) (stop func()) {
	served, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var serving sync.WaitGroup
	serving.Go(func() {
		if err := Serve(served, l, h, drain); err != nil {
			slog.ErrorContext(served, "background server stopped", "address", l.Addr().String(), "error", err)
		}
	})
	return func() {
		cancel()
		serving.Wait()
	}
}

// Checked holds every message a handler takes and sends to the rules its contract states.
func Checked() connect.HandlerOption {
	return connect.WithInterceptors(validate.NewInterceptor(validate.WithValidateResponses()))
}

// Introspect serves health checks and reflection for services on mux.
func Introspect(mux *http.ServeMux, services ...string) {
	mux.Handle(grpchealth.NewHandler(grpchealth.NewStaticChecker(services...)))
	reflector := grpcreflect.NewStaticReflector(services...)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
}

// Authorized lets through only requests carrying token, when there is one; health checks need none.
func Authorized(h http.Handler, token string) http.Handler {
	if token == "" {
		return h
	}
	refusal := connect.NewErrorWriter()
	health := "/" + grpchealth.HealthV1ServiceName + "/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		if strings.HasPrefix(r.URL.Path, health) || strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			h.ServeHTTP(w, r)
			return
		}
		_ = refusal.Write(w, r, connect.NewError(connect.CodeUnauthenticated, errors.New("this server asks for its bearer token")))
	})
}

// Bearer is a client carrying token on every request, when there is one.
func Bearer(token string) *http.Client {
	if token == "" {
		return http.DefaultClient
	}
	return &http.Client{Transport: bearing{token: token, next: http.DefaultTransport}}
}

type bearing struct {
	token string
	next  http.RoundTripper
}

func (b bearing) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// WarnOpen warns that a server listening on l beyond this machine answers anyone, when it has no token.
func WarnOpen(ctx context.Context, l net.Listener, token, key string) {
	if tcp, ok := l.Addr().(*net.TCPAddr); token == "" && (!ok || !tcp.IP.IsLoopback()) {
		slog.WarnContext(ctx, "listening beyond this machine without a token: anyone on the network may use it", "address", l.Addr().String(), "set", key)
	}
}
