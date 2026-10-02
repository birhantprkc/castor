package server

import (
	"net"
	"net/url"
	"testing"
)

func TestARendererFetchesWhatACastServesThroughTheServersMediaRouteAndTheSourceAsItIs(t *testing.T) {
	d := newDeliveries(&url.URL{Scheme: "http", Host: "192.168.1.20:8410"}, "cast")
	l, err := d.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())

	served := &url.URL{Scheme: "http", Host: l.Addr().String(), Path: "/stream.m3u8", RawQuery: "v=1"}
	if got, want := d.reached(served).String(), "http://192.168.1.20:8410/media/cast/"+port+"/stream.m3u8?v=1"; got != want {
		t.Errorf("what the cast serves is fetched at %s, want %s", got, want)
	}
	source := &url.URL{Scheme: "https", Host: "cdn.example", Path: "/movie.mp4"}
	if got := d.reached(source); got != source {
		t.Errorf("the source is fetched at %s, want it as is", got)
	}
}

func TestAClosedDeliveryIsNoLongerServed(t *testing.T) {
	d := newDeliveries(&url.URL{Scheme: "http", Host: "192.168.1.20:8410"}, "cast")
	l, err := d.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	if !d.serves(port) {
		t.Fatal("an open delivery was not served")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if d.serves(port) {
		t.Error("a closed delivery's port, free for anyone to take, is still served")
	}
}
