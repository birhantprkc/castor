package core

import (
	"testing"
	"time"

	"github.com/stupside/castor/internal/device"
)

// TestResolveInfoPinnedAddress confirms a configured device.Address takes the
// direct-connect branch: it yields an Info built from the address without any
// discovery round-trip (asserted here for Chromecast, whose resolution is pure).
func TestResolveInfoPinnedAddress(t *testing.T) {
	cfg := Config{
		Device:  device.Config{Type: device.TypeChromecast, Address: "192.168.0.10"},
		Network: NetworkConfig{Timeout: 5 * time.Second},
	}

	info, err := resolveInfo(t.Context(), cfg)
	if err != nil {
		t.Fatalf("resolveInfo() error = %v", err)
	}
	if info.Address != "192.168.0.10" {
		t.Errorf("Address = %q, want the pinned host", info.Address)
	}
	if info.Type != device.TypeChromecast {
		t.Errorf("Type = %q, want chromecast", info.Type)
	}
	if info.Name != "192.168.0.10" {
		t.Errorf("Name = %q, want the host as default label", info.Name)
	}
}
