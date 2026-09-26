// Package device is the port every renderer family implements, and the registry castor discovers and connects renderers through.
package device

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stupside/castor/internal/media"
)

type Type string

type codecEnvelope struct {
	profiles  []media.Profile
	bitDepths []int // nil == 8-bit only
	maxLevel  int
}

var codecEnvelopes = map[media.Codec]codecEnvelope{
	// Level 4.2 (1080p60) is what every HD H.264 decoder castor targets is built to.
	media.CodecH264: {profiles: []media.Profile{media.ProfileConstrainedBaseline, media.ProfileBaseline, media.ProfileMain, media.ProfileHigh}, maxLevel: 42},
	media.CodecHEVC: {profiles: []media.Profile{media.ProfileMain, media.ProfileMain10}, bitDepths: []int{8, 10}},
	media.CodecVP8:  {}, // VP8 has no profile split in Castor's probe model; 8-bit is the default.
}

// VideoSupport is the envelope every family states for codec.
func VideoSupport(codec media.Codec) media.VideoSupport {
	env := codecEnvelopes[codec]
	return media.VideoSupport{
		Codec:     codec,
		Profiles:  env.profiles,
		BitDepths: env.bitDepths,
		MaxLevel:  env.maxLevel,
	}
}

// Info names one renderer; as a cast target, an empty Address means discover it by Name.
type Info struct {
	Name    string
	Type    Type
	Address string
}

// Device is a connected renderer, ready to play. Obtain one via Connect.
type Device interface {
	// Play points the renderer at streamURL, advertised as contentType.
	Play(ctx context.Context, streamURL *url.URL, contentType string) error

	AwaitEnd(ctx context.Context) error

	Capabilities() media.Capabilities

	StreamHeaders(contentType string) map[string]string

	Close() error
}

// Family is one device family's strategy: everything protocol-specific behind a single interface.
type Family interface {
	// Type is the name the operator selects this family by.
	Type() Type

	SelfFetches() bool

	Discover(ctx context.Context) []Info

	// Locate resolves a pinned address into the one Connect dials.
	Locate(ctx context.Context, address string) (string, error)

	// Connect opens a session to the device at info.
	Connect(ctx context.Context, info Info) (Device, error)
}

// Registry is every family this process can reach, the single dispatch table, owned by the composition root.
type Registry []Family

func (r Registry) family(t Type) (Family, error) {
	for _, f := range r {
		if f.Type() == t {
			return f, nil
		}
	}
	return nil, fmt.Errorf("unknown device type: %q", t)
}

func (r Registry) Known(t Type) error {
	_, err := r.family(t)
	return err
}

func (r Registry) Profile(t Type) media.Capabilities {
	f, err := r.family(t)
	if err != nil {
		return media.Capabilities{}
	}
	return media.Capabilities{SelfFetch: f.SelfFetches()}
}

// hostLabel is the default label when config pins an address but names no device.
func hostLabel(address string) string {
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		address = u.Host
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

func (r Registry) Discover(ctx context.Context, timeout time.Duration) []Info {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	found := make([][]Info, len(r))
	var wg sync.WaitGroup
	for i, f := range r {
		wg.Go(func() { found[i] = f.Discover(ctx) })
	}
	wg.Wait()

	return slices.Concat(found...)
}

func (r Registry) Connect(ctx context.Context, target Info, timeout time.Duration) (Device, error) {
	f, err := r.family(target.Type)
	if err != nil {
		return nil, err
	}
	info, err := resolve(ctx, f, target, timeout)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "device found", "name", info.Name, "type", string(info.Type), "address", info.Address)

	dev, err := f.Connect(ctx, info)
	if err != nil {
		return nil, fmt.Errorf("connecting to device: %w", err)
	}
	slog.InfoContext(ctx, "connected to device", "name", info.Name)
	return dev, nil
}

// resolve pins target by its address, or else sweeps only its own family for its name.
func resolve(ctx context.Context, f Family, target Info, timeout time.Duration) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if target.Address != "" {
		slog.InfoContext(ctx, "pinning device by address", "name", target.Name, "type", string(target.Type), "address", target.Address)
		address, err := f.Locate(ctx, target.Address)
		if err != nil {
			return Info{}, fmt.Errorf("pinning device: %w", err)
		}
		return Info{Name: cmp.Or(target.Name, hostLabel(target.Address)), Type: target.Type, Address: address}, nil
	}

	slog.InfoContext(ctx, "discovering device", "name", target.Name, "type", string(target.Type))
	for _, d := range f.Discover(ctx) {
		if strings.EqualFold(d.Name, target.Name) {
			return d, nil
		}
	}
	return Info{}, fmt.Errorf("finding device: device %q (type %s) not found", target.Name, target.Type)
}
