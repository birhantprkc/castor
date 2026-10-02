package deliver

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Listeners opens the sockets deliveries serve on; whoever binds it decides where renderers reach them.
type Listeners interface {
	Listen(ctx context.Context) (net.Listener, error)
}

// serve answers the renderer on ln until the returned server closes; its requests carry ctx's values, not its end.
func serve(ctx context.Context, ln net.Listener, h http.Handler) *http.Server {
	base := context.WithoutCancel(ctx)
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return base }}
	go func() { _ = server.Serve(ln) }()
	return server
}
