package dlna

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestLocateTrustsADescriptionURL(t *testing.T) {
	const address = "http://192.168.0.5:9197/dmr/description.xml"
	got, err := Family{}.Locate(t.Context(), address)
	if err != nil {
		t.Fatalf("Locate() error = %v", err)
	}
	if got != address {
		t.Errorf("Locate() = %q, want the description URL itself, with no SSDP search", got)
	}
}

// A pinned bare address is located by a unicast M-SEARCH.
func TestSearchDLNADescriptionRoundTrip(t *testing.T) {
	const wantLocation = "http://127.0.0.1:9197/dmr/description.xml"

	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting fake SSDP responder: %v", err)
	}
	defer pc.Close()

	responderDone := make(chan struct{})
	go func() {
		defer close(responderDone)
		buf := make([]byte, 2048)
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if !strings.HasPrefix(string(buf[:n]), "M-SEARCH * HTTP/1.1") {
			t.Errorf("responder got non-M-SEARCH datagram: %q", buf[:n])
			return
		}
		resp := "HTTP/1.1 200 OK\r\n" +
			"Location: " + wantLocation + "\r\n" +
			"ST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n"
		if _, err := pc.WriteTo([]byte(resp), from); err != nil {
			t.Errorf("responder write: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	got, err := searchDLNADescription(ctx, pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("searchDLNADescription() error = %v", err)
	}
	if got != wantLocation {
		t.Errorf("searchDLNADescription() = %q, want %q", got, wantLocation)
	}
	<-responderDone
}
