// Package core holds the device-agnostic decision layer and the machinery every
// cast shares: config, source resolution, device discovery/connect, the pure facts a
// cast is composed from (Shape, Facts) with the copy-vs-encode decisions taken from
// them, and the served delivery driver.
//
// Device-agnostic is a statement about what core KNOWS, not about what it links.
// The edge runs core to device, not the other way: Connect returns a
// device.Device and Config embeds device.Config, because a cast has to be handed
// a connected renderer from somewhere and this is the layer that owns the
// prelude. What core never does is name a family. Every decision below reads
// media.Renderer capabilities, which the device adapters produce, so adding or
// changing a family cannot reach a decision here; and the device package imports
// nothing of core's, so no decision can leak the other way.
package core

import (
	"time"

	"github.com/stupside/castor/internal/cast/subtitle"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/source/resolve"
)

// Config is the device-neutral configuration the planner and every stage share:
// the target device, network, transcode binary, source resolver, and the
// subtitle transcriber. It composes only domain types, so core imports no
// application state; the app config assembles this and hands it in.
type Config struct {
	Device    DeviceConfig
	Network   NetworkConfig
	Transcode TranscodeConfig
	Resolver  resolve.Config

	// Source is the source resolver with its adapters already bound: it is handed
	// in rather than built here from Resolver, because a package that constructs
	// its own ffprobe subprocess and http.Client cannot be run without them, and
	// the judgements the resolver makes (which candidate is real, which rendition
	// to read) are exactly the ones worth testing. The composition root owns the
	// wiring; this layer owns none of it.
	Source *resolve.Resolver

	// Delivery is the operator's say over the delivery axis, read by the composition
	// rule that would otherwise hand the renderer the source URL (see
	// Shape.Passthrough). Unset (the zero value) means DeliveryAuto, so a cast nobody
	// configured is decided entirely from capabilities and the source.
	Delivery DeliveryPreference

	// Whisper is the subtitle-transcription knob the subtitle axis reads (Enable gates
	// burn-in, see SubtitleForServed). Its type lives in the cgo-free subtitle package,
	// not the whisper transcriber, so this decision layer carries it without importing
	// whisper's cgo. A renderer that fetches for itself never reaches the question, and
	// a disabled transcriber answers SubtitleOff.
	Whisper subtitle.Whisper
}

// DeviceConfig is the device section, owned by the device package so this
// device-agnostic layer neither defines nor names any device family. Core reads
// only its generic Name/Type (for discovery) and forwards the whole value to
// device.Connect, which alone interprets any family-specific field.
type DeviceConfig = device.Config

type NetworkConfig struct {
	Timeout   time.Duration `yaml:"timeout" validate:"required"`
	Interface string        `yaml:"interface"`
}

// TranscodeConfig holds the small set of ffmpeg settings that aren't decided by
// the plan. Codec/bitrate/format choices are resolved from capabilities (see the
// Resolve* functions); only the binary path and the upstream I/O timeout, which
// no capability can determine, come from config.
type TranscodeConfig struct {
	FFmpegPath string `yaml:"ffmpeg_path" validate:"required"`

	// RWTimeout is the mid-read deadline: how long one upstream read may stall before
	// it is abandoned and retried. It is an input to the read policy (see read.For)
	// rather than a flag value, because whether a source of a given shape can afford
	// to have a read abandoned partway through is not something a config file knows.
	RWTimeout time.Duration `yaml:"rw_timeout" validate:"required"`
}
