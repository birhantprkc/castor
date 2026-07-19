package device

import (
	"net/url"
	"testing"

	"github.com/huin/goupnp"
)

func TestDLNAInfo(t *testing.T) {
	rootDevice := func(name string) *goupnp.RootDevice {
		return &goupnp.RootDevice{Device: goupnp.Device{FriendlyName: name}}
	}
	location := func(host, path string) *url.URL {
		return &url.URL{Scheme: "http", Host: host, Path: path}
	}

	tests := []struct {
		name   string
		result goupnp.MaybeRootDevice
		want   Info
		ok     bool
	}{
		{
			name:   "named renderer with a location",
			result: goupnp.MaybeRootDevice{Root: rootDevice("Living Room TV"), Location: location("192.0.2.10:8200", "/rootDesc.xml")},
			want:   Info{Name: "Living Room TV", Type: TypeDLNA, Address: "http://192.0.2.10:8200/rootDesc.xml"},
			ok:     true,
		},
		{
			name:   "second renderer maps independently",
			result: goupnp.MaybeRootDevice{Root: rootDevice("Bedroom Speaker"), Location: location("192.0.2.20:49152", "/desc.xml")},
			want:   Info{Name: "Bedroom Speaker", Type: TypeDLNA, Address: "http://192.0.2.20:49152/desc.xml"},
			ok:     true,
		},
		{
			name:   "announcement without a root device is rejected",
			result: goupnp.MaybeRootDevice{Location: location("192.0.2.30:8200", "/desc.xml")},
			ok:     false,
		},
		{
			name:   "announcement without a location is rejected",
			result: goupnp.MaybeRootDevice{Root: rootDevice("Ghost")},
			ok:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := dlnaInfo(tt.result)
			if ok != tt.ok {
				t.Fatalf("dlnaInfo() ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("dlnaInfo() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
