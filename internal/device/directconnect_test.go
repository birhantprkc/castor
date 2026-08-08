package device

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestLocate(t *testing.T) {
	tests := []struct {
		name        string
		dtype       Type
		devName     string
		address     string
		wantName    string
		wantType    Type
		wantAddress string
	}{
		{
			name:        "chromecast bare host defaults label to host",
			dtype:       TypeChromecast,
			address:     "192.168.0.10",
			wantName:    "192.168.0.10",
			wantType:    TypeChromecast,
			wantAddress: "192.168.0.10",
		},
		{
			name:        "chromecast host:port preserved for cast groups",
			dtype:       TypeChromecast,
			devName:     "Kitchen Group",
			address:     "192.168.0.10:42000",
			wantName:    "Kitchen Group",
			wantType:    TypeChromecast,
			wantAddress: "192.168.0.10:42000",
		},
		{
			name:        "roku bare host normalised to ECP root",
			dtype:       TypeRoku,
			address:     "192.168.0.3",
			wantName:    "192.168.0.3",
			wantType:    TypeRoku,
			wantAddress: "http://192.168.0.3:8060",
		},
		{
			name:        "roku host:port keeps the given port",
			dtype:       TypeRoku,
			address:     "192.168.0.3:8888",
			wantName:    "192.168.0.3",
			wantType:    TypeRoku,
			wantAddress: "http://192.168.0.3:8888",
		},
		{
			name:        "roku full url reduced to ECP root",
			dtype:       TypeRoku,
			devName:     "Bedroom",
			address:     "http://192.168.0.3:8060/",
			wantName:    "Bedroom",
			wantType:    TypeRoku,
			wantAddress: "http://192.168.0.3:8060",
		},
		{
			name:        "dlna description url trusted verbatim",
			dtype:       TypeDLNA,
			address:     "http://192.168.0.5:9197/dmr/description.xml",
			wantName:    "192.168.0.5",
			wantType:    TypeDLNA,
			wantAddress: "http://192.168.0.5:9197/dmr/description.xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := Locate(t.Context(), tt.dtype, tt.devName, tt.address)
			if err != nil {
				t.Fatalf("Locate() error = %v", err)
			}
			if info.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", info.Name, tt.wantName)
			}
			if info.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", info.Type, tt.wantType)
			}
			if info.Address != tt.wantAddress {
				t.Errorf("Address = %q, want %q", info.Address, tt.wantAddress)
			}
		})
	}
}

func TestLocateUnknownType(t *testing.T) {
	if _, err := Locate(t.Context(), Type("beam"), "", "192.168.0.9"); err == nil {
		t.Fatal("Locate() with unknown type: want error, got nil")
	}
}

func TestParseSSDPLocation(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
	}{
		{
			name: "location extracted case-insensitively with colons in value",
			response: "HTTP/1.1 200 OK\r\n" +
				"CACHE-CONTROL: max-age=1800\r\n" +
				"Location: http://192.168.0.5:9197/dmr/description.xml\r\n" +
				"ST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n",
			want: "http://192.168.0.5:9197/dmr/description.xml",
		},
		{
			name:     "no location header",
			response: "HTTP/1.1 200 OK\r\nST: ssdp:all\r\n\r\n",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSSDPLocation([]byte(tt.response)); got != tt.want {
				t.Errorf("parseSSDPLocation() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSearchDLNADescriptionRoundTrip exercises the unicast M-SEARCH end to end
// against a loopback SSDP responder: it proves the connected-UDP send/receive/parse
// loop returns the device's LOCATION, and that the whole path uses only a dialed
// socket (no interface enumeration, which is what fails on Android/Termux).
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
			"CACHE-CONTROL: max-age=1800\r\n" +
			"LOCATION: " + wantLocation + "\r\n" +
			"ST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n"
		// Reply from the dialed address so the client's connected socket accepts it.
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

func TestHostOnly(t *testing.T) {
	tests := map[string]string{
		"192.168.0.3":                      "192.168.0.3",
		"192.168.0.3:8060":                 "192.168.0.3",
		"http://192.168.0.5:9197/desc.xml": "192.168.0.5",
		"http://192.168.0.5/desc.xml":      "192.168.0.5",
	}
	for address, want := range tests {
		if got := hostOnly(address); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", address, got, want)
		}
	}
}
