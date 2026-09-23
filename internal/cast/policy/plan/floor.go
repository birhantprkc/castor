package plan

import (
	"context"
	"fmt"
)

// Floor plans read-once encoding: which halves produce vs. pass through.
func Floor(ctx context.Context, in Inputs) (MediaPlan, error) {
	video, videoRefusals, err := floorVideoTrack(ctx, in)
	if err != nil {
		return MediaPlan{}, fmt.Errorf("planning the buffer's video: %w", err)
	}
	audio, audioRefusals := floorAudioTrack(in)
	return MediaPlan{Video: video, Audio: audio, Refusals: append(videoRefusals, audioRefusals...)}, nil
}

// Video half: stream copy or floor codec under VBV cap, scaled to ceiling.
func floorVideoTrack(ctx context.Context, in Inputs) (VideoTrack, []Refusal, error) {
	refused := refuse(videoCarriageRefusals, in)
	if len(refused) == 0 {
		return CopyVideo(), nil, nil
	}
	target := floorVideo
	enc, ok := in.Encoders(ctx, target.codec)
	if !ok {
		return VideoTrack{}, refused, fmt.Errorf("no working encoder for recovery codec %q", target.codec)
	}

	produced := VideoEncode{
		Encoder: enc,
		Maxrate: target.maxrate,
		Bufsize: target.bufsize,
		// Caps pixel rate where VBV cap cannot; prevents false slowness reports.
		MaxHeight: in.MaxHeight,
	}
	// Hardware uses bitrate, software uses quality; not interchangeable.
	if enc.Hardware {
		produced.Bitrate = target.bitrate
	} else {
		produced.Quality = floorVideoQuality
	}
	return EncodeVideo(produced), refused, nil
}

// Audio half: stream copy or stereo AAC; cannot fail (no host questions).
func floorAudioTrack(in Inputs) (AudioTrack, []Refusal) {
	refused := refuse(audioCarriageRefusals, in)
	if len(refused) == 0 {
		return CopyAudio(), nil
	}
	return EncodeAudio(AudioEncode{
		Codec:    floorAudio.codec,
		Bitrate:  floorAudio.bitrate,
		Channels: floorAudio.maxChannels,
	}), refused
}
