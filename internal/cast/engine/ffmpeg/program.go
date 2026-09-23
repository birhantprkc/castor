package ffmpeg

import (
	"fmt"

	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/media"
)

// ProgramSource binds execution-only read policies to inputs without leaking into media's source model.
type ProgramSource struct {
	program media.Program
	plan    read.Plan
}

// NewProgramSource snapshots a program and its complete read plan.
func NewProgramSource(program media.Program, plan read.Plan) (ProgramSource, error) {
	_, video := program.Track(media.TrackVideo)
	_, audio := program.Track(media.TrackAudio)
	if !video && !audio {
		return ProgramSource{}, fmt.Errorf("FFmpeg program source selects neither video nor audio")
	}
	if err := plan.Validate(program); err != nil {
		return ProgramSource{}, fmt.Errorf("FFmpeg program source: %w", err)
	}

	return ProgramSource{program: program.Clone(), plan: plan.Clone()}, nil
}

func (s ProgramSource) inputs() []sourceInput {
	inputs := make([]sourceInput, 0, len(s.program.Inputs))
	for _, input := range s.program.Inputs {
		inputs = append(inputs, sourceInput{
			url:         input.URL,
			headers:     input.Headers,
			contentType: input.ContentType,
			read:        s.plan[input.ID],
			offset:      s.program.Offsets[input.ID],
		})
	}
	return inputs
}

func (s ProgramSource) outputArgs() []string {
	if s.program.EndPolicy == media.EndAtShortest {
		return []string{"-shortest"}
	}
	return nil
}

func (s ProgramSource) track(kind media.TrackKind) (sourceTrack, bool) {
	ref, ok := s.program.Track(kind)
	if !ok {
		return sourceTrack{}, false
	}
	for index, input := range s.program.Inputs {
		if input.ID == ref.Input {
			return sourceTrack{input: index, index: ref.Index, optional: ref.Optional}, true
		}
	}
	// NewProgramSource validated this binding; unreachable without a programming error inside ProgramSource.
	return sourceTrack{}, false
}

func (s ProgramSource) ProbeInputs() []media.ProbeInput {
	inputs := s.inputs()
	out := make([]media.ProbeInput, 0, len(inputs))
	for i, input := range inputs {
		args := readArgs(input.read)
		args = append(args, media.HeaderArgs(input.headers)...)
		args = append(args, media.AdaptiveInputArgs(input.contentType, input.read.SegmentRetries)...)
		out = append(out, media.ProbeInput{ID: s.program.Inputs[i].ID, URL: input.url.String(), Args: args})
	}
	return out
}
