package probe

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// FFprobe is the ffprobe binary a cast measures its source and its local buffer with.
type FFprobe string

// Source binds an upstream to this ffprobe (opens exactly as reader will).
func (bin FFprobe) Source(program media.Program, inputs []ffmpeg.ProbeInput) media.Prober {
	return sourceProber{ffprobePath: string(bin), program: program, inputs: inputs}
}

type sourceProber struct {
	ffprobePath string
	program     media.Program
	inputs      []ffmpeg.ProbeInput
}

// Probe measures a demuxed program (audio in companion rendition; silence if audio can't be probed).
func (p sourceProber) Probe(ctx context.Context) (media.ProbeInfo, media.Reach, error) {
	program, inputs := p.program, p.inputs
	var info media.ProbeInfo
	video, hasVideo := selected(program, inputs, media.TrackVideo)
	audio, hasAudio := selected(program, inputs, media.TrackAudio)
	clockInput := slices.IndexFunc(inputs, func(in ffmpeg.ProbeInput) bool {
		return in.ID == program.ClockInput
	})

	// Open each selected input once; choose kind-relative tracks from complete JSON (signed URL single use).
	for inputIndex, input := range inputs {
		videoIndex, audioIndex := -1, -1
		if hasVideo && video.input == inputIndex {
			videoIndex = video.index
		}
		if hasAudio && audio.input == inputIndex {
			audioIndex = audio.index
		}
		if videoIndex < 0 && audioIndex < 0 && inputIndex != clockInput {
			continue
		}

		measured, reach, err := pass{
			ffprobePath: p.ffprobePath,
			inputArgs:   input.Args,
			input:       input.URL,
			videoIndex:  videoIndex,
			audioIndex:  audioIndex,
		}.run(ctx)
		if err != nil {
			switch {
			case videoIndex >= 0:
				return media.ProbeInfo{}, reach, fmt.Errorf("probing selected video input: %w", err)
			case audioIndex >= 0:
				return info, reach, fmt.Errorf("probing selected audio input: %w", err)
			default:
				return info, reach, fmt.Errorf("probing program clock input: %w", err)
			}
		}
		if info.InputStarts == nil {
			info.InputStarts = map[media.InputID]time.Duration{}
		}
		info.InputStarts[input.ID] = measured.Start
		if inputIndex == clockInput {
			info.Start = measured.Start
			info.ContentType = measured.ContentType
			info.BitRate = measured.BitRate
			info.Duration = measured.Duration
		}
		// The ladder travels with the video half (worth most where alternatives are measured).
		if videoIndex >= 0 {
			info = info.TakeVideo(measured)
		}
		if audioIndex >= 0 {
			info = info.TakeAudio(measured)
		}
	}

	if hasVideo && info.VideoCodec == "" && !video.optional {
		return info, media.ReachOpened, fmt.Errorf("selected video track V:%d is absent from input %d", video.index, video.input)
	}
	if hasAudio && info.AudioCodec == "" && !audio.optional {
		return info, media.ReachOpened, fmt.Errorf("selected audio track a:%d is absent from input %d", audio.index, audio.input)
	}
	return info, media.ReachOpened, nil
}

// selection is which input carries one selected track (input index, track index, optional).
type selection struct {
	input    int
	index    int
	optional bool
}

func selected(program media.Program, inputs []ffmpeg.ProbeInput, kind media.TrackKind) (selection, bool) {
	ref, ok := program.Track(kind)
	if !ok {
		return selection{}, false
	}
	// NewProgramSource validated that every track names one of the inputs.
	index := slices.IndexFunc(inputs, func(in ffmpeg.ProbeInput) bool { return in.ID == ref.Input })
	return selection{input: index, index: ref.Index, optional: ref.Optional}, true
}
