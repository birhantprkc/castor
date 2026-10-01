package execute

import (
	"github.com/stupside/castor/internal/cast/plan"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
)

// Config is what one attempt runs on (binaries, ceiling, renderer ports, burn-in).
type Config struct {
	// FFmpegPath is the binary every read and encode runs, and Binary what it understands.
	FFmpegPath string
	Binary     ffmpeg.Binary

	// Encoders is the host's encoder lookup, bound to that binary.
	Encoders plan.Encoders

	// Probes measures what an attempt reads: its source program, or its local buffer.
	Probes probe.FFprobe

	Renderer Renderer

	Listeners Listeners

	// Subtitles is bound by cmd (cgo transcriber); nil = no burn-in.
	Subtitles Subtitles

	// MaxHeight is the tallest picture the cast may show (user's instruction, not measured).
	MaxHeight media.HeightCap

	// Timelines serves the inputs whose timelines castor keeps.
	Timelines Timelines
}
