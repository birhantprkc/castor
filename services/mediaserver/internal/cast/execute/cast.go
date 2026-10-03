package execute

import (
	"context"
	"io"
	"net/url"

	"github.com/stupside/castor/services/mediaserver/internal/cast/codec"
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
	"github.com/stupside/castor/services/mediaserver/internal/cast/transcode"
	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/probe"
)

// Machinery is what every attempt of every cast runs on.
type Machinery struct {
	// FFmpegPath is the binary every read and encode runs, and Binary what it understands.
	FFmpegPath string
	Binary     ffmpeg.Binary

	// Encoders is the host's encoder lookup, bound to that binary.
	Encoders codec.Encoders

	// Probes measures what an attempt reads: its source program, or its local buffer.
	Probes probe.FFprobe

	// Timelines serves the inputs whose timelines castor keeps.
	Timelines Timelines

	// InputArgs is how ffmpeg opens an input, as its source format says.
	InputArgs transcode.InputArgs
}

// Cast is what one cast's attempts run with, on the machinery every cast shares.
type Cast struct {
	Machinery

	Device Device

	Listeners deliver.Listeners

	// Subtitles is nil for a cast that burns none in.
	Subtitles Subtitles

	// MaxHeight is the tallest picture the cast may show.
	MaxHeight media.HeightCap
}

// Device is what a cast plays on, connected for the whole cast by whoever lent it, who also closes it.
type Device interface {
	// Play points the device at streamURL, advertised as contentType.
	Play(ctx context.Context, streamURL *url.URL, contentType string) error

	AwaitEnd(ctx context.Context) error

	Capabilities() media.Capabilities
}

// Subtitles starts the burn-in a cast reading into workDir draws, nil when it cannot.
type Subtitles func(ctx context.Context, workDir string) Burn

// Burn is a transcription a cast feeds its sound and draws into its picture.
type Burn interface {
	// Lead is how far the transcription has committed, which the playback gate waits on.
	health.Lead

	// SampleRate is the rate of the mono PCM feed Run reads.
	SampleRate() int

	// Run transcribes the PCM feed until it ends, and closes it.
	Run(ctx context.Context, pcm io.ReadCloser)

	// Inputs is the file the encode draws the cues from.
	Inputs() (burnIn string, err error)

	// Follow shows each cue as the encoder's progress reaches it.
	Follow(ctx context.Context) func(media.Progress)
}

// Timelines republishes a program's followed inputs for one read; the func stops serving them.
type Timelines interface {
	Republish(ctx context.Context, program media.Program) (media.Program, func() error, error)
}
