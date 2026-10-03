package execute

import (
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/plan"
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
	Encoders plan.Encoders

	// Probes measures what an attempt reads: its source program, or its local buffer.
	Probes probe.FFprobe

	// Timelines serves the inputs whose timelines castor keeps.
	Timelines Timelines

	// InputArgs is how ffmpeg opens an input, as its source format says.
	InputArgs transcode.InputArgs
}

// Cast is what one cast's attempts run with.
type Cast struct {
	Device Device

	Listeners deliver.Listeners

	// Subtitles is nil for a cast that burns none in.
	Subtitles Subtitles

	// MaxHeight is the tallest picture the cast may show.
	MaxHeight media.HeightCap
}

type config struct {
	Machinery
	Cast
}
