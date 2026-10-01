package deliver

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Listen opens the socket a delivery serves on, where its renderer reaches it.
type Listen func() (net.Listener, error)

// Serve answers the renderer on ln until the returned server closes; its requests carry ctx's values, not its end.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) *http.Server {
	base := context.WithoutCancel(ctx)
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return base }}
	go func() { _ = server.Serve(ln) }()
	return server
}
