package plan

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/media"
)

// audioTarget is a re-encode target: the bitrate and channel ceiling the codec carries.
type audioTarget struct {
	codec       media.Codec
	bitrate     string
	maxChannels int
}

// floorAudio is the stereo codec every renderer decodes.
var floorAudio = audioTarget{codec: media.CodecAAC, bitrate: "256k", maxChannels: 2}

// surroundTargets is every surround codec castor encodes to, in preference order.
var surroundTargets = []audioTarget{
	{codec: media.CodecEAC3, bitrate: "384k", maxChannels: 6},
	{codec: media.CodecAC3, bitrate: "448k", maxChannels: 6},
}

var audioRefusals = slices.Concat([]refusal{{
	// Renderer's advertised support is codec AND channel count; 2.0 AC-3 is not 5.1 AC-3.
	reason: ReasonRendererAudio,
	why:    says("the renderer never advertised this audio codec at this channel count"),
	when:   func(in Inputs) bool { return !in.Caps.CanCopyAudio(in.Probe) },
}, {
	reason: ReasonSampleRate,
	why:    says(fmt.Sprintf("the audio is sampled above the %d Hz every renderer castor serves plays", media.PlaybackSampleRate)),
	when:   func(in Inputs) bool { return in.Probe.AudioSampleRate > media.PlaybackSampleRate },
}}, audioCarriageRefusals)

var audioCarriageRefusals = []refusal{{
	reason: ReasonSpliced,
	why:    says("the source is stitched from pieces encoded apart, whose sample rates and timestamps change at each seam"),
	when:   func(in Inputs) bool { return in.Spliced },
}, {
	reason: ReasonContainerAudio,
	why: func(in Inputs) string {
		_, why := container.Reason(in.Probe, in.Into)
		return why
	},
	when: func(in Inputs) bool { return container.Known(in.Probe, in.Into).Audio },
}, {
	reason: ReasonAudioCopyFailed,
	why:    says("a previous attempt's copy of this audio track broke upstream, so it is decoded instead"),
	when:   func(in Inputs) bool { return in.Decode.Audio },
}}

func decideAudio(in Inputs) (AudioTrack, []Refusal, error) {
	if in.Probe.AudioCodec == "" && in.Measured {
		return CopyAudio(), nil, nil
	}
	refused := refuse(audioRefusals, in)
	if len(refused) == 0 {
		return CopyAudio(), nil, nil
	}
	channels := cmp.Or(in.Probe.AudioChannels, 2)
	var targets []audioTarget
	if channels > 2 {
		// Surround first so its channels survive; stereo AAC before a surround codec for a stereo source.
		targets = append(slices.Clone(surroundTargets), floorAudio)
	} else {
		targets = append([]audioTarget{floorAudio}, surroundTargets...)
	}
	for _, t := range targets {
		if !in.Caps.SupportsAudioCodec(t.codec) {
			continue
		}
		return EncodeAudio(AudioEncode{
			Codec:      t.codec,
			Bitrate:    t.bitrate,
			SampleRate: encodeSampleRate,
			// Never below stereo: the floor is stereo, and a surround codec is not asked to carry mono.
			Channels: max(min(channels, t.maxChannels), 2),
			Resync:   in.Spliced,
		}), refused, nil
	}
	return AudioTrack{}, refused, fmt.Errorf("no supported audio codec for re-encoding %q", in.Probe.AudioCodec)
}

const encodeSampleRate = media.PlaybackSampleRate
