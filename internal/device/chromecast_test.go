package device

import (
	"net"
	"testing"

	castdns "github.com/vishen/go-chromecast/dns"
)

func TestChromecastInfo(t *testing.T) {
	tests := []struct {
		name  string
		entry castdns.CastEntry
		want  Info
		ok    bool
	}{
		{
			name:  "ipv4 with friendly name on default port",
			entry: castdns.CastEntry{DeviceName: "Office Display", AddrV4: net.ParseIP("192.0.2.10"), Port: chromecastPort},
			want:  Info{Name: "Office Display", Type: TypeChromecast, Address: "192.0.2.10"},
			ok:    true,
		},
		{
			name:  "cast group on non-default port keeps host:port",
			entry: castdns.CastEntry{DeviceName: "Speakers", AddrV4: net.ParseIP("192.0.2.20"), Port: 32541},
			want:  Info{Name: "Speakers", Type: TypeChromecast, Address: "192.0.2.20:32541"},
			ok:    true,
		},
		{
			name:  "ipv6 used when no ipv4 is advertised",
			entry: castdns.CastEntry{DeviceName: "Living Room", AddrV6: net.ParseIP("2001:db8::1"), Port: chromecastPort},
			want:  Info{Name: "Living Room", Type: TypeChromecast, Address: "2001:db8::1"},
			ok:    true,
		},
		{
			name:  "name falls back to the mDNS instance name",
			entry: castdns.CastEntry{Name: "Kitchen", AddrV4: net.ParseIP("192.0.2.30"), Port: chromecastPort},
			want:  Info{Name: "Kitchen", Type: TypeChromecast, Address: "192.0.2.30"},
			ok:    true,
		},
		{
			name:  "entry without an address is rejected",
			entry: castdns.CastEntry{DeviceName: "Office Display"},
			ok:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := chromecastInfo(tt.entry)
			if ok != tt.ok {
				t.Fatalf("chromecastInfo() ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("chromecastInfo() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
