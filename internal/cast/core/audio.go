package core

import (
	"context"
	"log/slog"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// Audio delivery is device-neutral: both the spool path and the network remux
// path re-encode audio only when they must, and both decide it the same way,
// from a probe of the source plus the renderer's advertised audio support. The
// bitrate targets here are just good audio points, not tied to any device
// (unlike DecideVideo's targets, which are the renderer's decode budget).

// audioTarget is a surround re-encode target: the bitrate and the channel
// ceiling the codec carries, so a source above it (a 7.1 track targeting AC-3)
// folds down to what the codec supports instead of failing the encode.
type audioTarget struct {
	bitrate     string
	maxChannels int
}

// audioTargets is the re-encode target per surround codec, used when a
// multichannel source can't be stream-copied but the renderer advertises a Dolby
// codec (see DecideAudio). Bitrates are the common 5.1 broadcast/disc points.
// maxChannels is 6 (5.1) for both: ffmpeg's native ac3 and eac3 encoders reject
// more than 5.1 (they don't implement E-AC-3's 7.1 dependent substreams), so a
// 7.1 source folds to 5.1 rather than failing the encode. Stereo AAC is the
// floor below these and is not a target here. Adding a surround codec the
// pipeline encodes to is one entry here.
var audioTargets = map[media.Codec]audioTarget{
	media.CodecAC3:  {bitrate: "448k", maxChannels: 6},
	media.CodecEAC3: {bitrate: "384k", maxChannels: 6},
}

// audioCodecPreference ranks surround re-encode targets, most efficient first.
// DecideAudio consults it only for a multichannel source that can't be copied:
// the first codec the renderer advertises wins, keeping the 5.1/7.1 layout
// instead of downmixing. Stereo AAC is the floor below this and needs no entry.
var audioCodecPreference = []media.Codec{media.CodecEAC3, media.CodecAC3}

// AudioInputs is everything the audio decision reads, the audio counterpart to
// VideoInputs. There is no policy here: both served shapes want the same answer,
// because changing the wrapper is not a reason to touch the audio either.
type AudioInputs struct {
	// Caps is the connected renderer's advertised audio support.
	Caps media.Renderer
	// Probe measures the mapped audio track the encode will read.
	Probe media.ProbeInfo
	// Into is the container this encode writes.
	Into media.FormatInfo
	// Decode is the axes a previous attempt of this cast proved must not be copied,
	// because the reader copying them exited on the bitstream. Zero is the ordinary
	// case: a first attempt has no evidence against the source's own packets.
	Decode carriage.Axes
}

// DecideAudio is the whole copy-vs-encode answer for the audio axis, from three
// facts: the renderer's advertised support, the probe of the mapped track, and
// the container the encode will write.
//
//  1. copy: the renderer decodes the source codec and channel count AND the
//     output container can actually carry that codec, so a 5.1/7.1 track passes
//     through untouched, no quality loss and no downmix. What a copy then needs to
//     survive muxing (a framing repack, a delayed header) is not decided here: it
//     is derived inside ffmpeg.EncodeArgs from the same probe and format, so a
//     bitstream filter cannot exist without the copy it belongs to;
//  2. surround re-encode: a multichannel source the renderer cannot copy is
//     re-encoded to a Dolby codec it advertises (E-AC-3, else AC-3), keeping the
//     layout up to that codec's channel ceiling;
//  3. stereo AAC: the floor every renderer decodes, when neither applies (also
//     what a set advertising nothing surround-capable, or a failed probe's zero
//     ProbeInfo, gets).
//
// The container question in step 1 is the difference between adapting and dying.
// It asks nothing about the renderer and adds no ceiling: it asks only whether
// ffmpeg's muxer for this container has a stream type for this codec. The answer
// is sometimes no in a way that never surfaces: FLAC, Vorbis and PCM copied into
// MPEG-TS exit 0 with megabytes written and no audio stream at all in the output,
// and aac_latm, wmav2 and pcm_mulaw into mp4 or hls/fMP4 exit 234 before a byte.
// Both are fixed the same way, by falling through to the ladder below, which
// carries every one of them. The source is never refused and never rejected for
// being unusual; it is re-encoded.
//
// It takes ctx, which the mutator it replaces did not, so the one decision log
// line here correlates to the cast like every other.
func DecideAudio(ctx context.Context, in AudioInputs) ffmpeg.AudioTrack {
	blocked := carriage.Known(in.Probe, in.Into).Audio
	if in.Decode.Audio {
		// Stated where it is read rather than folded into blocked, because the container
		// refusing a codec and a bitstream that already killed a reader of this cast are
		// different failures and only one of them is about the muxer.
		slog.InfoContext(ctx, "a previous attempt's copy of this audio track broke upstream; decoding it instead",
			"codec", string(in.Probe.AudioCodec))
	} else if in.Caps.CanCopyAudio(in.Probe) {
		if !blocked {
			return ffmpeg.CopyAudio()
		}
		// As on the video axis, a refusal that came from the artifact has no reason
		// beyond the muxer having produced nothing for the track.
		_, why := carriage.Reason(in.Probe, in.Into)
		slog.InfoContext(ctx, "the output container will not carry this audio track; re-encoding",
			"codec", string(in.Probe.AudioCodec),
			"container", in.Into.ContentType,
			"reason", why)
	}
	if in.Probe.AudioChannels > 2 {
		for _, codec := range audioCodecPreference {
			t, ok := audioTargets[codec]
			if !ok || !in.Caps.SupportsAudioCodec(codec) {
				continue
			}
			return ffmpeg.EncodeAudio(ffmpeg.AudioEncode{
				Codec:      codec,
				Bitrate:    t.bitrate,
				SampleRate: encodeSampleRate,
				Channels:   min(in.Probe.AudioChannels, t.maxChannels),
			})
		}
	}
	return ffmpeg.EncodeAudio(ffmpeg.AudioEncode{
		Codec:      media.CodecAAC,
		Bitrate:    media.FloorAudioBitrate,
		SampleRate: encodeSampleRate,
		Channels:   2,
	})
}

// encodeSampleRate is the rate every re-encode targets. 48 kHz is what the
// Dolby codecs are specified at and what every renderer in the capability model
// decodes, so resampling to it costs nothing and removes a source-dependent
// variable from the encode.
const encodeSampleRate = 48000
