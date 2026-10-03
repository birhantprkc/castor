package dlna

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

// answerSearches answers unicast M-SEARCHes on a UDP port with the description's LOCATION, as a device's SSDP stack does.
func answerSearches(t *testing.T, description string) (string, error) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listening for SSDP searches: %w", err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { conn.Close(); <-done })
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if !strings.HasPrefix(string(buf[:n]), "M-SEARCH") {
				continue
			}
			reply := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLOCATION: " + description +
				"\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\nUSN: uuid:castor-e2e::urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n"
			_, _ = conn.WriteTo([]byte(reply), from)
		}
	}()
	return conn.LocalAddr().String(), nil
}
