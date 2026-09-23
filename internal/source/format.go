package source

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/stupside/castor/internal/media"
)

// opaque reads a source whole: the fallback for what no format claims, never listed in Formats.
type opaque struct{}

func (opaque) Name() string { return "opaque" }

func (opaque) Identity() Identity { return Identity{} }

func (opaque) Resolve(_ context.Context, _ Env, s Subject) (Resolution, error) {
	return Opaque(s.Stream, s.Origin)
}

func (opaque) Recognize(string) (Ladder, []string) { return LadderUnknown, nil }

// containers are the files opaque reads whole, by the names a link announces them under.
var containers = []Identity{
	{ContentType: media.MP4, Extensions: []string{".mp4"}, MIMETypes: []string{"video/mp4"}},
	{ContentType: media.MKV, Extensions: []string{".mkv"}, MIMETypes: []string{"video/x-matroska"}},
	{ContentType: media.WebM, Extensions: []string{".webm"}, MIMETypes: []string{"video/webm"}},
	{ContentType: media.AVI, Extensions: []string{".avi"}, MIMETypes: []string{"video/x-msvideo"}},
	{ContentType: media.MOV, Extensions: []string{".mov"}, MIMETypes: []string{"video/quicktime"}},
	{ContentType: media.MPEGTS, MIMETypes: []string{"video/mp2t"}},
}

// Opaque: complete program reading; one URL names entire source.
func Opaque(stream Candidate, origin Origin) (Resolution, error) {
	program, err := ProgramFor(&stream)
	if err != nil {
		return Resolution{Origin: origin}, fmt.Errorf("normalizing source program: %w", err)
	}
	// Liveness and framing come from the origin, not the link.
	for i := range program.Inputs {
		program.Inputs[i].Fetch.Live = origin.Live
		program.Inputs[i].Fetch.Framing = origin.Framing
	}
	return Resolution{Program: program, Origin: origin}, nil
}

// Narrow narrows source to rung; measurement wins, then declaration.
func Narrow(program media.Program, chosen Rendition, from media.Program) (media.Program, error) {
	// Video index unconditional; measurement cleared by rebuild.
	tracks := slices.Clone(program.Tracks)
	for i := range tracks {
		if tracks[i].Kind == media.TrackVideo {
			tracks[i].Index = chosen.Index
		}
	}
	narrowed, err := media.NewProgram(media.Program{
		Inputs: program.Inputs, Tracks: tracks,
		ClockInput: program.ClockInput,
		Offsets:    program.Offsets,
		EndPolicy:  program.EndPolicy,
	})
	if err != nil {
		return media.Program{}, err
	}
	switch measured, ok := from.Measurement(); {
	case ok && from.SameBindings(narrowed):
		narrowed.SetMeasurement(measured)
	case chosen.Declared != nil:
		narrowed.SetMeasurement(*chosen.Declared)
	}
	return narrowed, nil
}

// ReportRendition reports rung about to be read; handles missing ceiling.
func ReportRendition(ctx context.Context, chosen Rendition, origin Origin, ceiling media.HeightCap) {
	level, msg := slog.LevelInfo, "rendition selected"
	if !ceiling.Admits(chosen.Height) {
		level, msg = slog.LevelWarn, "no rendition under the height cap; reading the shortest on offer and scaling it down"
	}
	slog.Log(ctx, level, msg,
		"height", chosen.Height, "cap", int(ceiling), "declared_bitrate", int64(chosen.Bitrate),
		"video_stream", chosen.Index,
		"renditions", len(origin.Renditions), "sole", origin.Sole())
}
