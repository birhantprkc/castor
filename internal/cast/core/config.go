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
	"github.com/stupside/castor/internal/media"
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

	// MaxHeight is the tallest picture this cast may put in front of the renderer, and
	// it is the user's instruction rather than anything measured or negotiated. It is a
	// term of the CAST and not of an encode, which is why it sits here and not on
	// TranscodeConfig: it decides a composition as well (a source known to be taller
	// cannot be handed over untouched, see Shape.Passthrough) and it bounds a copy as well
	// as a scale. What the number means is media.HeightCap's to state.
	//
	// The operator writes it under `resolver:`, where the ranker reads it to prefer the
	// largest variant that fits. It arrives here as a value of its own rather than inside
	// that whole section, so there is exactly one place in this struct to read the ceiling
	// from and no path by which a stage reads a second copy of it.
	MaxHeight media.HeightCap

	// Source is the source resolver with its adapters already bound: it is handed in
	// rather than built here, because a package that constructs its own ffprobe
	// subprocess and http.Client cannot be run without them, and the judgements the
	// resolver makes (which candidate is real, which rendition to read) are exactly the
	// ones worth testing. The composition root owns the wiring; this layer owns none of
	// it, and there is one resolver per process so the client that probed a candidate is
	// the client that fetches its playlist.
	Source *resolve.Resolver

	// Delivery is the operator's say over the delivery axis, read by the composition
	// rule that would otherwise hand the renderer the source URL (see
	// Shape.Passthrough). Unset (the zero value) means media.DeliveryAuto, so a cast nobody
	// configured is decided entirely from capabilities and the source.
	Delivery media.DeliveryPreference

	// Whisper is the subtitle-transcription knob, and Enable is the whole of the subtitle
	// axis: the composition root builds a burn-in stage when it is set and none when it is
	// not (see cast.burnInStage). Its type lives in the cgo-free subtitle package, not the
	// whisper transcriber, so this decision layer carries it without importing whisper's
	// cgo. A renderer that fetches for itself never reaches the question at all, because
	// only the composition castor produces the picture for has frames to draw cues into.
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
// the plan. Codec/bitrate/format choices are decided from capabilities (see DecideVideo
// and DecideAudio); only the binary path and the upstream I/O timeout, which no
// capability can determine, come from config.
type TranscodeConfig struct {
	FFmpegPath string `yaml:"ffmpeg_path" validate:"required"`

	// FFprobePath is the measurement binary, beside the transcoding one because both are
	// the same kind of fact: which executable this install runs. It carries no YAML tag
	// because the operator states it once, under `resolver:`, where the source layer's own
	// prober is built from it; the composition root copies it here rather than letting a
	// cast layer reach into the source resolver's configuration section for it.
	FFprobePath string `yaml:"-"`

	// RWTimeout is the mid-read deadline: how long one upstream read may stall before
	// it is abandoned and retried. It is an input to the read policy (see read.For)
	// rather than a flag value, because whether a source of a given shape can afford
	// to have a read abandoned partway through is not something a config file knows.
	RWTimeout time.Duration `yaml:"rw_timeout" validate:"required"`
}
