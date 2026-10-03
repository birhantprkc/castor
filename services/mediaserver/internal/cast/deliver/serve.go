package deliver

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Listeners opens the sockets deliveries serve on; whoever binds it decides where devices reach them.
type Listeners interface {
	Listen(ctx context.Context) (net.Listener, error)
}

// serve answers the device on ln until the returned server closes; its requests carry ctx's values, not its end.
func serve(ctx context.Context, ln net.Listener, h http.Handler) *http.Server {
	base := context.WithoutCancel(ctx)
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return base }}
	go func() { _ = server.Serve(ln) }()
	return server
}

// closeGrace is how long a closing delivery lets the responses it already wrote finish going out.
const closeGrace = 2 * time.Second

// stop ends server once its responses have gone out whole, cutting what still runs after closeGrace.
func stop(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), closeGrace)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return server.Close()
	}
	return nil
}
