package plan

import (
	"cmp"
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

// Audio half: stream copy, else surround kept in the buffer's surround codec and stereo in AAC; cannot fail.
func floorAudioTrack(in Inputs) (AudioTrack, []Refusal) {
	refused := refuse(audioCarriageRefusals, in)
	if len(refused) == 0 {
		return CopyAudio(), nil
	}
	// The renderer is not known yet, so the buffer keeps what a surround set could still be given.
	target := floorAudio
	if in.Probe.AudioChannels > floorAudio.maxChannels {
		target = surroundTargets[0]
	}
	return EncodeAudio(AudioEncode{
		Codec:      target.codec,
		Bitrate:    target.bitrate,
		SampleRate: encodeSampleRate,
		Channels:   max(min(cmp.Or(in.Probe.AudioChannels, 2), target.maxChannels), 2),
		Resync:     in.Spliced,
	}), refused
}
