package server

import (
	"context"
	"net"
	"net/url"
	"sync"
)

// deliveries is what one cast serves: loopback listeners its media route reaches while they are open.
type deliveries struct {
	route *url.URL

	mu   sync.Mutex
	open map[string]bool
}

func newDeliveries(server *url.URL, cast string) *deliveries {
	return &deliveries{route: server.JoinPath("media", cast), open: map[string]bool{}}
}

func (d *deliveries) Listen(context.Context) (net.Listener, error) {
	l, err := net.Listen("tcp", loopback("0"))
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.open[l.Addr().String()] = true
	return admitted{Listener: l, deliveries: d}, nil
}

// admitted is a delivery's listener; closing it shuts the route to its port, which the kernel may hand anyone next.
type admitted struct {
	net.Listener
	deliveries *deliveries
}

func (a admitted) Close() error {
	a.deliveries.mu.Lock()
	delete(a.deliveries.open, a.Addr().String())
	a.deliveries.mu.Unlock()
	return a.Listener.Close()
}

// reached is where a renderer fetches u: through the media route if this cast serves it, else as is.
func (d *deliveries) reached(u *url.URL) *url.URL {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.open[u.Host] {
		return u
	}
	at := d.route.JoinPath(u.Port(), u.Path)
	at.RawQuery = u.RawQuery
	return at
}

func (d *deliveries) serves(port string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.open[loopback(port)]
}

func loopback(port string) string { return net.JoinHostPort("127.0.0.1", port) }
