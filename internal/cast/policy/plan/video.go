package plan

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/stupside/castor/internal/container"
	"github.com/stupside/castor/internal/media"
)

var videoCarriageRefusals = []refusal{{
	// Container carriage asks whether ffmpeg's muxer has a stream type for the codec.
	reason: ReasonContainerVideo,
	why: func(in Inputs) string {
		why, _ := container.Reason(in.Probe, in.Into)
		return why
	},
	when: func(in Inputs) bool { return container.Known(in.Probe, in.Into).Video },
}, {
	// Kept apart from container's refusal because they are different failures with different lifetimes.
	reason: ReasonVideoCopyFailed,
	why:    says("a previous attempt's copy of this video track broke upstream, so it is decoded instead"),
	when:   func(in Inputs) bool { return in.Decode.Video },
}}

var videoRefusals = slices.Concat([]refusal{{
	// The renderer's advertised envelope is the only thing that makes a copy safe at all.
	reason: ReasonRendererVideo,
	why:    says("the renderer never advertised this video envelope"),
	when:   func(in Inputs) bool { return !in.Caps.CanCopyVideo(in.Probe) },
}}, videoCarriageRefusals, []refusal{{
	// The ceiling is the user's request and may not be bypassed; cost is stated after encoder is known.
	reason: ReasonHeightLimit,
	why:    says("the source picture is taller than this cast's ceiling admits"),
	when:   func(in Inputs) bool { return !in.MaxHeight.Admits(in.Probe.VideoHeight) },
}, {
	reason: ReasonHDRPolicy,
	why:    says("nothing establishes that an arbitrary set engages HDR on a stream it was handed"),
	when:   func(in Inputs) bool { return in.Probe.VideoHDR },
}, {
	// A cue drawn into the picture needs decoded frames; a copy has no frames to draw on.
	reason: ReasonSubtitleBurnIn,
	why:    says("this leg burns subtitles into the picture, which needs decoded frames to draw on"),
	when:   func(in Inputs) bool { return in.BurnIn != "" },
}})

func decideVideo(ctx context.Context, in Inputs) (VideoTrack, []Refusal, error) {
	refused := refuse(videoRefusals, in)
	if len(refused) == 0 {
		return CopyVideo(), nil, nil
	}
	host := func(c media.Codec) (Encoder, bool) { return in.Encoders(ctx, c) }
	enc, t, ok := selectVideoEncoder(in.Caps, host)
	if !ok {
		return VideoTrack{}, refused, noVideoEncoder(in.Caps, host)
	}
	return EncodeVideo(VideoEncode{
		Encoder:             enc,
		Bitrate:             t.bitrate,
		Maxrate:             t.maxrate,
		Bufsize:             t.bufsize,
		MaxHeight:           in.MaxHeight,
		KeyframeIntervalSec: keyframeSeconds,
		SubtitleTextFile:    in.BurnIn,
	}), refused, nil
}

func selectVideoEncoder(caps media.Capabilities, selectEncoder func(media.Codec) (Encoder, bool)) (Encoder, videoTarget, bool) {
	for _, t := range videoTargets {
		if !caps.SupportsCodec(t.codec) {
			continue
		}
		if enc, ok := selectEncoder(t.codec); ok && holdsRealtime(enc, t.codec) {
			return enc, t, true
		}
	}
	// No common strategy is safer than inventing support the renderer never advertised.
	return Encoder{}, videoTarget{}, false
}

func holdsRealtime(enc Encoder, codec media.Codec) bool {
	return enc.Hardware || codec == media.CodecH264
}

func noVideoEncoder(caps media.Capabilities, selectEncoder func(media.Codec) (Encoder, bool)) error {
	// Deduplicated; a family may list one codec several times and a user learns nothing three times.
	advertised := make([]string, 0, len(caps.Video))
	for _, s := range caps.Video {
		if !slices.Contains(advertised, string(s.Codec)) {
			advertised = append(advertised, string(s.Codec))
		}
	}
	// Asked over EVERY target rather than only renderer-wanted ones; each proof is cached for the process.
	producible := make([]string, 0, len(videoTargets))
	declined := make([]string, 0, len(videoTargets))
	for _, t := range videoTargets {
		codec := t.codec
		enc, ok := selectEncoder(codec)
		switch {
		case !ok:
		case holdsRealtime(enc, codec):
			producible = append(producible, string(codec))
		case caps.SupportsCodec(codec):
			declined = append(declined,
				fmt.Sprintf("%s is available here only as the software encoder %s, which castor does not run live", codec, enc.Name))
		}
	}
	why := ""
	if len(declined) > 0 {
		why = " (" + strings.Join(declined, "; ") + ")"
	}
	return fmt.Errorf("no video encoder for this renderer: it decodes %s, this host can encode %s, and the two do not meet%s",
		cmp.Or(strings.Join(advertised, ", "), "no video codec at all"),
		cmp.Or(strings.Join(producible, ", "), "none of the codecs castor targets"),
		why)
}

type videoTarget struct {
	codec                     media.Codec
	bitrate, maxrate, bufsize string
}

// floorVideo is the codec every renderer decodes, and the one a read-once buffer is produced in.
var floorVideo = videoTarget{codec: media.CodecH264, bitrate: "4M", maxrate: "4M", bufsize: "8M"}

// videoTargets is every codec castor encodes video to, in preference order.
var videoTargets = []videoTarget{
	{codec: media.CodecHEVC, bitrate: "2M", maxrate: "2M", bufsize: "4M"},
	floorVideo,
}

const keyframeSeconds = 2

const floorVideoQuality = 23
