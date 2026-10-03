package container

import (
	"slices"
	"strings"

	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Known reports axes a container can't carry (skip failing attempts).
func Known(probe media.ProbeInfo, into FormatInfo) media.Axes {
	_, video := match(videoRules, probe.VideoCodec, probe, into)
	_, audio := match(audioRules, probe.AudioCodec, probe, into)
	return media.Axes{Video: video, Audio: audio}
}

// rule is one measured pair a container won't carry.
type rule struct {
	why  string // Log message distinguishing container vs device refusal.
	when func(codec media.Codec, probe media.ProbeInfo, into FormatInfo) bool
}

// match finds first applicable rule from table.
func match(rules []rule, codec media.Codec, probe media.ProbeInfo, into FormatInfo) (rule, bool) {
	if codec == "" {
		return rule{}, false // no track, nothing to carry
	}
	i := slices.IndexFunc(rules, func(r rule) bool { return r.when(codec, probe, into) })
	if i < 0 {
		return rule{}, false
	}
	return rules[i], true
}

// inBand and outOfBand match declared framing.
func inBand(into FormatInfo) bool    { return into.Framing == media.FramingInBand }
func outOfBand(into FormatInfo) bool { return into.Framing == media.FramingOutOfBand }

var audioRules = []rule{{
	// MPEG-TS writes unsupported codecs as unreadable private data.
	why: "the container has no stream type for this codec and would write it as unreadable private data",
	when: func(c media.Codec, _ media.ProbeInfo, into FormatInfo) bool {
		return inBand(into) && (c == media.CodecFLAC || c == media.CodecVorbis || isPCM(c))
	},
}, {
	// The mp4 family refuses what it cannot carry, loudly and before a single byte.
	why: "the container has no tag for this codec and would refuse to write a header",
	when: func(c media.Codec, _ media.ProbeInfo, into FormatInfo) bool {
		return outOfBand(into) && slices.Contains(
			[]media.Codec{"aac_latm", "wmav2", "pcm_mulaw", "pcm_alaw"}, c)
	},
}, {
	// The one rule here that no artifact can reveal.
	why: "TrueHD in a fragmented mp4 on a pipe is only sliced correctly when a video track is mapped alongside it",
	when: func(c media.Codec, probe media.ProbeInfo, into FormatInfo) bool {
		return c == media.CodecTrueHD && into.Muxer == ffmpeg.FormatMP4 && probe.VideoCodec == ""
	},
}}

var videoRules = []rule{{
	// MPEG-TS carries four video codecs: H.264, HEVC, MPEG-2 and MPEG-4 part 2.
	why: "the container has no stream type for this codec and would write it as unreadable private data",
	when: func(c media.Codec, _ media.ProbeInfo, into FormatInfo) bool {
		return inBand(into) && slices.Contains([]media.Codec{
			media.CodecVP8, media.CodecVP9, media.CodecAV1,
			media.CodecMJPEG, "msmpeg4v3",
		}, c)
	},
}, {
	// The codecs the mp4 family has no tag for.
	why: "the container has no tag for this codec and would refuse to write a header",
	when: func(c media.Codec, _ media.ProbeInfo, into FormatInfo) bool {
		return outOfBand(into) && slices.Contains([]media.Codec{
			media.CodecVP8, "msmpeg4v3",
			"theora", "flv1", "vp6f", "wmv1", "wmv2", "wmv3", "vc1",
			"h263", "h263p", "prores", "dvvideo", "rv40", "svq3",
		}, c)
	},
}}

func Reason(probe media.ProbeInfo, into FormatInfo) (video, audio string) {
	if r, ok := match(videoRules, probe.VideoCodec, probe, into); ok {
		video = r.why
	}
	if r, ok := match(audioRules, probe.AudioCodec, probe, into); ok {
		audio = r.why
	}
	return video, audio
}

// isPCM matches every PCM codec by ffprobe's pcm_ prefix; MPEG-TS treats them alike.
func isPCM(c media.Codec) bool { return strings.HasPrefix(string(c), "pcm_") }
