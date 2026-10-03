package mediaroute

import (
	"net"
	"net/url"
	"testing"
)

func TestAClosedDeliveryIsNoLongerServed(t *testing.T) {
	d := NewDeliveries(&url.URL{Scheme: "http", Host: "192.168.1.20:8410"}, "cast")
	l, err := d.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	if !d.Serves(port) {
		t.Fatal("an open delivery was not served")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if d.Serves(port) {
		t.Error("a closed delivery's port, free for anyone to take, is still served")
	}
}
