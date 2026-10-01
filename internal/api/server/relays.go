package server

import (
	"context"
	"net"
	"sync"
)

// relays is what one cast serves: loopback listeners only its clients' relay reaches, and only those.
type relays struct {
	cast string

	mu   sync.Mutex
	open map[string]bool
}

func newRelays(cast string) *relays { return &relays{cast: cast, open: map[string]bool{}} }

// Listen opens a delivery on loopback and admits it to the relay.
func (r *relays) Listen(context.Context) (net.Listener, error) {
	l, err := net.Listen("tcp", loopback("0"))
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.open[l.Addr().String()] = true
	return l, nil
}

// path is where a client relays address, if this cast opened it.
func (r *relays) path(address string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open[address] {
		return "", false
	}
	_, port, _ := net.SplitHostPort(address)
	return "relay/" + r.cast + "/" + port, true
}

func (r *relays) serves(port string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open[loopback(port)]
}

func loopback(port string) string { return net.JoinHostPort("127.0.0.1", port) }
