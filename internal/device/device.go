// Package device discovers media renderers on the local network and speaks
// their control protocols (DLNA/UPnP AVTransport, Chromecast). The Device
// interface is deliberately small: the cast pipeline decides what to send
// (subtitles are burned in upstream), a Device only needs to fetch and play.
package device

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stupside/castor/internal/media"
)

type Type string

const (
	TypeDLNA       Type = "dlna"
	TypeChromecast Type = "chromecast"
	TypeRoku       Type = "roku"
)

// Profile is what is knowable about a renderer of this type before there is one: the
// static self-fetch protocol fact, and nothing else. It is capability DATA and not a
// second kind of answer, so the layer above reads the same media.Renderer shape in both
// phases of a cast and needs no per-family question of its own.
//
// Every other field is deliberately zero, and zero means unmeasured everywhere in that
// record (see media.Renderer's conventions). That is what makes composing a cast before
// connecting sound rather than a guess: a rule reading a field no profile fills cannot be
// answered here, which is precisely the rule that has to wait for the renderer.
//
// The one fact it does answer agrees with the connected renderer's Capabilities().SelfFetch
// by construction: each family's media.Renderer literal sets SelfFetch by calling its own
// selfFetches() rather than re-stating the bool, so the two cannot drift (pinned by
// TestProfileAgreesWithWhatEachFamilyReportsConnected). A cast composed on this fact and
// contradicted by the connected device would read the single-use source on terms the
// renderer refuses, having already spent the URL.
//
// An unregistered type answers the zero renderer: not self-fetching, nothing measured.
// Nothing can be cast to it anyway, and Connect is where that is reported.
func Profile(t Type) media.Renderer {
	r, ok := rendererFor(t)
	if !ok {
		return media.Renderer{}
	}
	return media.Renderer{SelfFetch: r.selfFetches()}
}

// codecEnvelope is the codec-fixed part of a stream-copy envelope: the profiles
// and bit depths that black-screen a renderer that cannot handle them (10-bit
// H.264 is the rare High 10 profile, which the sets in this package's families
// decode as a green smear or not at all). Adding a codec the pipeline can encode
// to is one entry here.
//
// It lives in this file rather than with any one family because all three read
// it: DLNA pairs it with whatever a renderer answered over GetProtocolInfo, while
// Chromecast and Roku pair it with the codecs their vendor publishes. What varies
// between families is WHICH codecs are decoded; what a given codec's safe
// envelope is does not vary, and stating it three times is how two of them end up
// disagreeing about High 10.
type codecEnvelope struct {
	profiles  []string
	bitDepths []int // nil == 8-bit only
}

var codecEnvelopes = map[media.Codec]codecEnvelope{
	media.CodecH264: {profiles: []string{"Constrained Baseline", "Baseline", "Main", "High"}},
	media.CodecHEVC: {profiles: []string{"Main", "Main 10"}, bitDepths: []int{8, 10}},
}

// videoSupportFor builds the copy envelope for a codec: its decode-safety
// profile and bit-depth constraints.
func videoSupportFor(codec media.Codec) media.VideoSupport {
	env := codecEnvelopes[codec]
	return media.VideoSupport{
		Codec:     codec,
		Profiles:  env.profiles,
		BitDepths: env.bitDepths,
	}
}

type Info struct {
	Name    string
	Type    Type
	Address string
}

// Config is the resolved device target the agnostic layers carry and forward:
// which renderer to reach (Name, Type) plus an opaque Family payload. It names no
// device family, so core/cast and every other device carry it without knowing any
// one family exists. The composition root (internal/config) fills Family with the
// selected family's typed connect settings; only that family's strategy reads it,
// via a type assertion in its own connect. Family is nil for a family that needs none.
type Config struct {
	Name string
	Type Type

	// Address, when set, pins where the renderer is so a cast connects to it
	// directly and skips discovery. It is what the user configured: a bare host or
	// host:port for any family, or a DLNA device description URL. Locate turns it
	// into the concrete Info each family's connect needs. Empty means "find me by
	// Name over discovery".
	Address string

	Family any
}

// Device is a connected renderer, ready to play. Obtain one via Connect.
type Device interface {
	// Play points the renderer at streamURL, advertised as contentType.
	Play(ctx context.Context, streamURL *url.URL, contentType string) error

	// Capabilities reports what this renderer can play: the containers it
	// accepts as-is and the video envelopes it decodes natively. Each device
	// resolves this from itself at connect time (DLNA negotiates it over
	// GetProtocolInfo; Chromecast reports its known receiver profile), so the
	// copy-vs-encode decision follows what the renderer advertises rather than
	// an assumption baked in per device type.
	Capabilities() media.Renderer

	// StreamHeaders returns protocol-specific HTTP headers the local stream
	// server must send when this renderer fetches contentType. Nil when the
	// protocol needs none.
	StreamHeaders(contentType string) map[string]string

	Close() error
}

// renderer is one device family's strategy: everything protocol-specific about
// reaching a renderer of that family, behind a single interface. Discovering a
// device, locating a pinned one, and connecting are all family-specific, so they
// live here rather than as switches scattered across the package. The registry
// below is the one place a family is wired in and the sole dispatch table, so no
// operation carries a per-family type switch; adding a family is implementing this
// interface and registering it.
type renderer interface {
	// selfFetches reports whether the family fetches media URLs itself (a smart
	// client) rather than only playing bytes castor serves it. It is a static
	// protocol property answered without a device in hand (see Profile).
	selfFetches() bool

	// discover scans the local network for devices of this family until ctx expires,
	// contributing no devices (rather than erroring) when the scan fails.
	discover(ctx context.Context) []Info

	// locate turns a pre-known address into a connectable Info, skipping discovery
	// (see Locate). name defaults to the host when empty.
	locate(ctx context.Context, name, address string) (Info, error)

	// connect opens a session to the device at info. cfg carries the family's opaque
	// connect settings (Config.Family), asserted and read only by this family.
	connect(ctx context.Context, info Info, cfg Config) (Device, error)
}

// renderers is the family registry: the single wiring point for renderer families
// and the sole dispatch table for every family-specific operation. Registry order
// is the discovery/scan listing order.
var renderers = []struct {
	Type Type
	renderer
}{
	{TypeDLNA, dlna{}},
	{TypeChromecast, chromecast{}},
	{TypeRoku, roku{}},
}

// rendererFor returns the strategy registered for a device type.
func rendererFor(t Type) (renderer, bool) {
	for _, r := range renderers {
		if r.Type == t {
			return r.renderer, true
		}
	}
	return nil, false
}

// Connect opens a session to the renderer described by info, dispatching to its
// family strategy. cfg is forwarded blindly from upstream; only the target
// family's connect reads its own settings out of cfg.Family.
func Connect(ctx context.Context, info Info, cfg Config) (Device, error) {
	r, ok := rendererFor(info.Type)
	if !ok {
		return nil, fmt.Errorf("unknown device type: %q", info.Type)
	}
	return r.connect(ctx, info, cfg)
}

func FindInfo(ctx context.Context, timeout time.Duration, dtype Type, name string) (Info, error) {
	devices, err := Discover(ctx, timeout)
	if err != nil {
		return Info{}, err
	}
	for _, d := range devices {
		if d.Type == dtype && strings.EqualFold(d.Name, name) {
			return d, nil
		}
	}
	return Info{}, fmt.Errorf("device %q (type %s) not found", name, dtype)
}

// Locate builds a connectable Info for a renderer whose address is known ahead of
// time, dispatching to its family strategy so the cast skips discovery entirely.
// This is the direct-connect path: it serves renderers reachable by unicast but not
// by discovery, whose SSDP/mDNS is multicast and so crosses no VLAN and is refused
// where interface enumeration is denied (Android/Termux, where discovery fails with
// "route ip+net: netlinkrib: permission denied"); it also shortcuts the discovery
// window for a device at a stable address.
//
// address is what the user pinned: a bare host or host:port for any family, or, for
// DLNA, its device description URL. Each family turns that into the concrete Info
// its connect needs; DLNA additionally resolves a bare host to a description URL
// over a unicast SSDP request. name is carried through as the label and defaults to
// the host, since a pinned device is not found by name.
func Locate(ctx context.Context, dtype Type, name, address string) (Info, error) {
	r, ok := rendererFor(dtype)
	if !ok {
		return Info{}, fmt.Errorf("unknown device type: %q", dtype)
	}
	return r.locate(ctx, name, address)
}

// hostOnly reduces a pinned address to its host, for use as a default label when
// the config pins an address but names no device.
func hostOnly(address string) string {
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		address = u.Host
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// Discover scans the local network for renderers of every registered family in
// parallel (DLNA via SSDP MediaRenderer, Chromecast via mDNS _googlecast._tcp,
// Roku via SSDP roku:ecp), sharing one timeout window; a family that fails
// contributes no devices rather than failing the whole scan. Results follow the
// registry order.
func Discover(ctx context.Context, timeout time.Duration) ([]Info, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	found := make([][]Info, len(renderers))
	var wg sync.WaitGroup
	for i, r := range renderers {
		wg.Go(func() { found[i] = r.discover(ctx) })
	}
	wg.Wait()

	return slices.Concat(found...), nil
}
