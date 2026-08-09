package core

import (
	"testing"
	"time"

	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
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

// TestHandoffPossibleReadsBothHalvesOfTheQuestion pins the one bit the source layer is
// told before it decides whether to spend a second open of a single-use URL. Both halves
// have to refuse for the answer to be right: dropping the capability half puts every
// push-only cast back to paying for a fact no composition reads, and dropping the
// operator half does the same to every configured relay.
func TestHandoffPossibleReadsBothHalvesOfTheQuestion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		device   device.Type
		delivery media.DeliveryPreference
		want     bool
	}{
		{"a self-fetching renderer with the decision left to the evidence", device.TypeChromecast, media.DeliveryAuto, true},
		{"an unset knob means the same thing as auto", device.TypeChromecast, "", true},
		{"a renderer castor has to serve is never handed a URL", device.TypeDLNA, media.DeliveryAuto, false},
		{"an operator asking for a relay has answered for every source", device.TypeChromecast, media.DeliveryServe, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Device: device.Config{Type: tc.device}, Delivery: tc.delivery}
			if got := HandoffPossible(cfg); got != tc.want {
				t.Errorf("HandoffPossible = %v, want %v", got, tc.want)
			}
		})
	}
}
