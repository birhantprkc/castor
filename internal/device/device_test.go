package device

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stub is a family whose every answer is scripted, so the registry's own dispatch runs.
type stub struct {
	name       Type
	discovered []Info
	sweeps     *int
}

func (s stub) Type() Type        { return s.name }
func (s stub) SelfFetches() bool { return false }

func (s stub) Discover(context.Context) []Info {
	if s.sweeps != nil {
		*s.sweeps++
	}
	return s.discovered
}

func (s stub) Locate(_ context.Context, address string) (string, error) { return address, nil }

func (s stub) Connect(context.Context, Info) (Device, error) {
	return nil, errors.New("stub renderers do not connect")
}

var registry = Registry{
	stub{name: "push"},
	stub{name: "pull", discovered: []Info{{Name: "Living Room", Type: "pull", Address: "192.168.0.7"}}},
}

func TestAPinnedAddressSkipsDiscovery(t *testing.T) {
	sweeps := 0
	push := stub{name: "push", sweeps: &sweeps}
	for _, tt := range []struct {
		target Info
		want   string
	}{
		{Info{Type: "push", Address: "http://192.168.0.5:9197/desc.xml"}, "192.168.0.5"},
		{Info{Type: "push", Address: "192.168.0.3:8060", Name: "Bedroom"}, "Bedroom"},
	} {
		info, err := resolve(t.Context(), push, tt.target, time.Second)
		if err != nil {
			t.Fatalf("resolve(%+v) error = %v", tt.target, err)
		}
		if info != (Info{Name: tt.want, Type: "push", Address: tt.target.Address}) {
			t.Errorf("resolve(%+v) = %+v, want it labelled %q", tt.target, info, tt.want)
		}
	}
	if sweeps != 0 {
		t.Errorf("a pinned address swept the network %d times", sweeps)
	}
}

func TestDiscoveryFindsTheNamedDeviceWithinItsFamilyOnly(t *testing.T) {
	info, err := resolve(t.Context(), registry[1], Info{Type: "pull", Name: "living room"}, time.Second)
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if info.Address != "192.168.0.7" {
		t.Errorf("resolve() = %+v, want the discovered device, matched case-insensitively", info)
	}
	if _, err := registry.Connect(t.Context(), Info{Type: "push", Name: "Living Room"}, time.Second); err == nil {
		t.Error("a device of another family was matched by name alone")
	}

	sweeps := 0
	r := Registry{stub{name: "slow", sweeps: &sweeps}, registry[1]}
	_, _ = r.Connect(t.Context(), Info{Type: "pull", Name: "Living Room"}, time.Second)
	if sweeps != 0 {
		t.Error("finding a pull device swept another family too")
	}
}
