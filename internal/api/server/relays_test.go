package server

import (
	"net"
	"testing"
)

func TestAClosedDeliveryIsNoLongerRelayed(t *testing.T) {
	r := newRelays("cast")
	l, err := r.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	if !r.serves(port) {
		t.Fatal("an open delivery was not relayed")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if r.serves(port) {
		t.Error("a closed delivery's port, free for anyone to take, is still relayed")
	}
}
