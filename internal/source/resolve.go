// Package source: Ranker (which candidates worth attempting, measures); Resolver (what source publishes).
package source

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stupside/castor/internal/media"
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

// NewResolver binds resolution to the playlists fetcher and the formats it reads.
func NewResolver(cfg Config, playlists Playlists, formats Formats) *Resolver {
	return &Resolver{
		env:     Env{Playlists: playlists, MaxHeight: cfg.MaxHeight},
		formats: formats,
	}
}

// RefetchProgram resolves a link, narrowed to chosen when a rung was already picked (zero for none).
func (r *Resolver) RefetchProgram(ctx context.Context, stream *Candidate, chosen Rendition) (Resolution, error) {
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
	format := r.formats.claiming(stream)
	resolved, err := format.Resolve(ctx, r.env, Subject{Stream: *stream, Origin: origin, Chosen: chosen})
	if err != nil {
		return resolved, err
	}
	slog.InfoContext(ctx, "source shape resolved", "shape", format.Name())
	return resolved, nil
}

// ProgramFor expands one opaque candidate into a single-input program (format builds richer graph).
func ProgramFor(candidate *Candidate) (media.Program, error) {
	if candidate == nil {
		return media.Program{}, fmt.Errorf("source candidate is nil")
	}

	// Source candidate has only container-level fetch metadata; preserve only container-level facts.
	fetch := media.Fetch{Segmented: media.IsSegmented(candidate.ContentType)}
	inputs := []media.Input{{
		ID: media.PrimaryInputID, URL: candidate.URL, Headers: candidate.Headers,
		ContentType: candidate.ContentType, Fetch: fetch,
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
	if candidate.Probe != nil {
		program.SetMeasurement(*candidate.Probe)
	}
	return program, nil
}
