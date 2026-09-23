package execute

import (
	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/media"
)

// Config is what one attempt runs on (binaries, ceiling, renderer ports, burn-in).
type Config struct {
	// FFmpegPath is the binary every read and encode runs.
	FFmpegPath string

	// Encoders is the host's encoder lookup, bound to that binary.
	Encoders plan.Encoders

	Probes Probes

	Renderer Renderer

	Addresses Addresses

	// Subtitles is bound by cmd (cgo transcriber); nil = no burn-in.
	Subtitles Subtitles

	// MaxHeight is the tallest picture the cast may show (user's instruction, not measured).
	MaxHeight media.HeightCap
}
