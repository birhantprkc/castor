package source

import (
	"context"
	"fmt"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/timeline"
)

// opaque reads a source whole: the reader of what no format claims.
type opaque struct{}

// Resolve reads the whole source behind one URL.
func (opaque) Resolve(_ context.Context, _ Env, s Subject) (Resolution, error) {
	program, err := ProgramFor(&s.Stream)
	if err != nil {
		return Resolution{Origin: s.Origin}, fmt.Errorf("normalizing source program: %w", err)
	}
	// Liveness and framing come from the origin, not the link.
	for i := range program.Inputs {
		program.Inputs[i].Fetch.Live = s.Origin.Live
		program.Inputs[i].Fetch.Framing = s.Origin.Framing
	}
	return Resolution{Program: program, Origin: s.Origin}, nil
}

func (opaque) Timeline(Client, media.Input, media.TrackKind) timeline.Source { return nil }

// containers are the files opaque reads whole, by the names a link announces them under.
var containers = []Identity{
	{ContentType: media.MP4, Extensions: []string{".mp4"}, MIMETypes: []string{media.MP4}},
	{ContentType: media.MKV, Extensions: []string{".mkv"}, MIMETypes: []string{media.MKV}},
	{ContentType: media.WebM, Extensions: []string{".webm"}, MIMETypes: []string{media.WebM}},
	{ContentType: media.AVI, Extensions: []string{".avi"}, MIMETypes: []string{media.AVI}},
	{ContentType: media.MOV, Extensions: []string{".mov"}, MIMETypes: []string{media.MOV}},
	{ContentType: media.MPEGTS, MIMETypes: []string{media.MPEGTS}},
}
