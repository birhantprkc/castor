package deliver

import (
	"net"
	"net/http"
	"time"
)

// Listen binds a delivery to the address the renderer reaches castor at, on a port of the kernel's choosing.
func Listen(localIP string) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(localIP, "0"))
}

// Serve answers the renderer on ln until the returned server closes.
func Serve(ln net.Listener, h http.Handler) *http.Server {
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(ln) }()
	return server
}
