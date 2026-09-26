package source

import (
	"cmp"
	"context"
	"log/slog"
	"net/url"

	"github.com/stupside/castor/internal/media"
)

// Described is program carrying what is known of the rung it reads: a measurement of that very reading, else the rung's declaration.
func Described(program media.Program, chosen Rendition, from media.Program) (media.Program, error) {
	// A rebuilt program starts unmeasured.
	described, err := media.NewProgram(media.Program{
		Inputs: program.Inputs, Tracks: program.Tracks,
		ClockInput: program.ClockInput,
		Offsets:    program.Offsets,
		EndPolicy:  program.EndPolicy,
	})
	if err != nil {
		return media.Program{}, err
	}
	switch measured, ok := from.Measurement(); {
	case ok && from.SameBindings(described):
		described.SetMeasurement(measured)
	case chosen.Declared != nil:
		described.SetMeasurement(*chosen.Declared)
	}
	return described, nil
}

// ReportRendition reports rung about to be read; handles missing ceiling.
func ReportRendition(ctx context.Context, chosen Rendition, origin Origin, ceiling media.HeightCap) {
	level, msg := slog.LevelInfo, "rendition selected"
	if !ceiling.Admits(chosen.Height) {
		level, msg = slog.LevelWarn, "no rendition under the height cap; reading the shortest on offer and scaling it down"
	}
	slog.Log(ctx, level, msg,
		"height", chosen.Height, "cap", int(ceiling), "declared_bitrate", int64(chosen.Bitrate),
		"representation", chosen.Representation,
		"renditions", len(origin.Renditions), "sole", origin.Sole())
}

// Rendition is one version of a program a source offered, as the source described it.
type Rendition struct {
	URL *url.URL

	// Representation names the rung inside a manifest that publishes its ladder behind one URL.
	Representation string

	// AudioURL is the companion audio rendition for this rung.
	AudioURL *url.URL

	// Bitrate is the rate the source declared, 0 when it declared none.
	Bitrate media.Bitrate

	// Height is the declared display height, 0 when the source omitted it.
	Height int

	// Declared is the codec envelope the source declared for this rung.
	Declared *media.ProbeInfo
}

func (r Rendition) BackedBy(prior Rendition) Rendition {
	if r.AudioURL == nil {
		r.AudioURL = prior.AudioURL
	}
	if r.Declared == nil {
		r.Declared = prior.Declared
	}
	return r
}

func SelfFetchHeight(program media.Program, origin Origin, chosen Rendition) int {
	// A rung with a URL of its own was narrowed at the URL, so the renderer is pinned to it.
	if chosen.URL != nil {
		return cmp.Or(chosen.Height, program.MeasuredHeight())
	}
	// Otherwise the URL still names the whole ladder. Take the tallest rung it publishes.
	tallest := 0
	for _, rung := range origin.Renditions {
		tallest = max(tallest, rung.Height)
	}
	return cmp.Or(tallest, chosen.Height, program.MeasuredHeight())
}
