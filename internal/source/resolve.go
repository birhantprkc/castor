// Package source turns a link into the program a cast reads: what the origin publishes, and the formats that read it.
package source

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/timeline"
)

// Resolution is what source resolution established about one link.
type Resolution struct {
	// Program is what castor will read (later stages derive execution plans from it).
	Program media.Program

	// Origin is what the source offered (must stay true after choice was made).
	Origin Origin

	// Rendition is the choice made from ladder (zero if source published no ladder).
	Rendition Rendition
}

// Resolver is resolution with adapters bound (measures/fetches nothing itself; testable).
type Resolver struct {
	env     Env
	formats Formats
}

// sniffBytes is enough of a body to read a playlist's or manifest's grammar, and far less than a film.
const sniffBytes = 64 << 10

// Identify names what a link carries: its name first, else the grammar of its body, since a script serves a playlist under any name.
func (r *Resolver) Identify(ctx context.Context, u *url.URL) string {
	if ct := r.formats.ContentTypeOf(u, ""); ct != "" {
		return ct
	}
	head := http.Header{"Range": {timeline.Range{Length: sniffBytes}.Header()}}
	body, _, _, err := r.env.Client.Fetch(ctx, u, head)
	if err != nil {
		return ""
	}
	return r.formats.sniff(body)
}

// NewResolver binds resolution to the client that reads origins, the tallest picture a cast shows and the formats it reads.
func NewResolver(client Client, maxHeight media.HeightCap, formats Formats) *Resolver {
	return &Resolver{
		env:     Env{Client: client, MaxHeight: maxHeight},
		formats: formats,
	}
}

// RefetchProgram resolves a link, narrowed to chosen when a rung was already picked (zero for none).
func (r *Resolver) RefetchProgram(ctx context.Context, stream *Stream, chosen Rendition) (Resolution, error) {
	if stream == nil || stream.URL == nil {
		return Resolution{}, fmt.Errorf("source has no URL")
	}
	origin := Origin{}
	if probe := stream.Probe; probe != nil {
		origin.Duration = probe.Duration
		origin.Live = probe.Duration == 0
	}
	// Adaptive manifests are segmented even without format-specific parser.
	origin.Segmented = media.IsSegmented(stream.ContentType)
	resolved, err := r.formats.Claiming(stream.ContentType).Resolve(ctx, r.env, Subject{Stream: *stream, Origin: origin, Chosen: chosen})
	if err != nil {
		return resolved, err
	}
	if drm := resolved.Origin.Protection; drm != "" {
		return resolved, fmt.Errorf("the source is protected by DRM (%s), which castor cannot decrypt", drm)
	}
	slog.InfoContext(ctx, "source resolved", "content_type", stream.ContentType)
	return resolved, nil
}

// ProgramFor expands one opaque stream into a single-input program (format builds richer graph).
func ProgramFor(stream *Stream) (media.Program, error) {
	// A stream has only container-level fetch metadata; preserve only container-level facts.
	fetch := media.Fetch{Segmented: media.IsSegmented(stream.ContentType)}
	inputs := []media.Input{{
		ID: media.PrimaryInputID, URL: stream.URL, Headers: stream.Headers,
		ContentType: stream.ContentType, Fetch: fetch,
	}}
	tracks := []media.TrackRef{
		{Input: media.PrimaryInputID, Kind: media.TrackVideo, Optional: true},
		{Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true},
	}
	program, err := media.NewProgram(media.Program{
		Inputs: inputs, Tracks: tracks,
		ClockInput: media.PrimaryInputID,
		EndPolicy:  media.EndAtLongest,
	})
	if err != nil {
		return media.Program{}, err
	}
	if stream.Probe != nil {
		program.SetMeasurement(*stream.Probe)
	}
	return program, nil
}
