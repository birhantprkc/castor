package mediaroute

import (
	"context"
	"net"
	"net/url"
	"sync"
)

// Deliveries is what one cast serves: loopback listeners the route reaches while they are open.
type Deliveries struct {
	route *url.URL

	mu   sync.Mutex
	open map[string]bool
}

// NewDeliveries opens cast's deliveries to devices reaching this server at reach.
func NewDeliveries(reach *url.URL, cast string) *Deliveries {
	return &Deliveries{route: reach.JoinPath("media", cast), open: map[string]bool{}}
}

func (d *Deliveries) Listen(context.Context) (net.Listener, error) {
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
	deliveries *Deliveries
}

func (a admitted) Close() error {
	a.deliveries.mu.Lock()
	delete(a.deliveries.open, a.Addr().String())
	a.deliveries.mu.Unlock()
	return a.Listener.Close()
}

// Reached is where a device fetches u: through the route if this cast serves it, else as is.
func (d *Deliveries) Reached(u *url.URL) *url.URL {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.open[u.Host] {
		return u
	}
	at := d.route.JoinPath(u.Port(), u.Path)
	at.RawQuery = u.RawQuery
	return at
}

// Serves reports whether a delivery of this cast listens on port.
func (d *Deliveries) Serves(port string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.open[loopback(port)]
}

func loopback(port string) string { return net.JoinHostPort("127.0.0.1", port) }
