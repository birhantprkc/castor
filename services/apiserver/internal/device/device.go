// Package device is the port every device family implements, the registry reaching them, and the directory the API lists and lends them from.
package device

import (
	"context"
	"fmt"
	"net/url"
	"slices"

	"google.golang.org/protobuf/proto"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
)

// Type names a device family, as the contract's Device.type and Target.Pinned.type do.
type Type string

// Device is a connected device, ready to play; whoever connected it closes it.
type Device interface {
	// Play points the device at streamURL, packaged as container.
	Play(ctx context.Context, streamURL *url.URL, container mediav1.Container) error

	AwaitEnd(ctx context.Context) error

	Capabilities() *mediav1.Capabilities

	Close() error
}

// Gone is a device established unreachable.
type Gone struct {
	// Device is the device's name, or where castor reached it if it has none.
	Device string

	// Observed is the family's account of how it established this, for humans to read.
	Observed string

	// Err is the last failure the family saw.
	Err error
}

func (g *Gone) Error() string {
	msg := fmt.Sprintf("device %q is unreachable", g.Device)
	if g.Observed != "" {
		msg += ": " + g.Observed
	}
	if g.Err != nil {
		msg += ": " + g.Err.Error()
	}
	return msg
}

func (g *Gone) Unwrap() error { return g.Err }

// MIME is how a device protocol names container, as the contract states it; empty for one it does not know.
func MIME(container mediav1.Container) string {
	v := container.Descriptor().Values().ByNumber(container.Number())
	if v == nil {
		return ""
	}
	return proto.GetExtension(v.Options(), mediav1.E_Mime).(string)
}

type codecEnvelope struct {
	profiles  []mediav1.Profile
	bitDepths []uint32 // nil == 8-bit only
	maxLevel  uint32
}

var codecEnvelopes = map[mediav1.Codec]codecEnvelope{
	// Level 4.2 (1080p60) is what every HD H.264 decoder castor targets is built to.
	mediav1.Codec_CODEC_H264: {profiles: []mediav1.Profile{mediav1.Profile_PROFILE_CONSTRAINED_BASELINE, mediav1.Profile_PROFILE_BASELINE, mediav1.Profile_PROFILE_MAIN, mediav1.Profile_PROFILE_HIGH}, maxLevel: 42},
	mediav1.Codec_CODEC_HEVC: {profiles: []mediav1.Profile{mediav1.Profile_PROFILE_MAIN, mediav1.Profile_PROFILE_MAIN_10}, bitDepths: []uint32{8, 10}},
	mediav1.Codec_CODEC_VP8:  {}, // VP8 has no profile split in Castor's probe model; 8-bit is the default.
}

// VideoSupport is the envelope every family states for codec.
func VideoSupport(codec mediav1.Codec) *mediav1.VideoSupport {
	env := codecEnvelopes[codec]
	return &mediav1.VideoSupport{
		Codec:     codec,
		Profiles:  slices.Clone(env.profiles),
		BitDepths: slices.Clone(env.bitDepths),
		MaxLevel:  env.maxLevel,
	}
}

// Info names one device, where it answers.
type Info struct {
	// ID is the device's own identity, the same on every discovery and unique within its family.
	ID      string
	Name    string
	Type    Type
	Address string
}

// Family is one device family's strategy: everything protocol-specific behind a single interface.
type Family interface {
	// Type is the name the contract selects this family by.
	Type() Type

	// Discover is every device answering now, each with its ID; one announced twice may repeat.
	Discover(ctx context.Context) []Info

	// Locate resolves a pinned address into the one Connect dials.
	Locate(ctx context.Context, address string) (string, error)

	// Connect opens a session to the device at info.
	Connect(ctx context.Context, info Info) (Device, error)
}
