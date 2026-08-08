// Package carriage answers one question and owns everything about it: can this
// container carry this track as it is, and if not, which half of the program has
// to be re-encoded.
//
// It exists because that question used to have no owner. The knowledge was spread
// across four packages: the containers' framing in the domain types, the
// uncarriable pairs in the ffmpeg adapter, the error type in the decision layer,
// and the recovery loops in the executor. Changing one rule meant touching all
// four, which is the definition of a concept without a home, and every bug this
// layer has had came from the seams between them.
//
// The answer is a table, consulted before anything runs, and that is the whole
// mechanism. There was briefly more: a runtime verdict that probed what castor had
// produced, compared it to what went in, and re-encoded the refused axis on a
// second attempt. It was four hundred lines across five packages, it needed the
// spool to be rewindable and the playback gate to wait on it, and every bug this
// layer had came from it. What it bought over the table was the codecs nobody had
// measured yet.
//
// So the table is the mechanism, and extending it is the fix. A codec it does not
// know fails loudly, with ffmpeg's own message in the log, and becomes one more
// row here. That is a worse failure mode on paper and a better one in practice: it
// is diagnosable, it cannot half-work, and it cannot silently make every cast
// slower or wrong the way its replacement did twice.
//
// Nothing here reads what ffmpeg printed. The CLI has no machine-readable error
// channel (every one of these is AVERROR(EINVAL) truncated to exit 234, and the
// only structured interface is the C API), and a strategy keyed on English prose
// is a contract nobody offered.
package carriage

import (
	"slices"

	"github.com/stupside/castor/internal/media"
)

// Axes is which halves of a program have to be re-encoded rather than copied. It
// is the only thing this package returns, because it is the only thing any caller
// can act on.
type Axes struct {
	Video bool
	Audio bool
}

// Any reports whether either half needs re-encoding.
func (a Axes) Any() bool { return a.Video || a.Audio }

// Or is both demands to re-encode at once. The two arrive from different places and
// neither overrules the other: what this container is known not to carry is a fact
// about the muxer, and what a previous attempt's copy broke on is a fact about the
// bitstream, so an axis either of them names is an axis to decode.
func (a Axes) Or(b Axes) Axes { return Axes{Video: a.Video || b.Video, Audio: a.Audio || b.Audio} }

// Copying is the halves a reader told to re-encode a will pass through untouched, and
// therefore the halves a failure of that reader can be about: only a copied axis can
// break on a bitstream filter it was handed, since a produced one is produced to the
// floor and the floor is carriable by definition.
func (a Axes) Copying() Axes { return Axes{Video: !a.Video, Audio: !a.Audio} }

// String names the axes for a log line and for the refusal a blamed copy produces,
// where it is what says which half of the program a recovery is about to spend
// processor time on.
func (a Axes) String() string {
	switch {
	case a.Video && a.Audio:
		return "video and audio"
	case a.Video:
		return "video"
	case a.Audio:
		return "audio"
	default:
		return "neither axis"
	}
}

// Known reports the axes a container is already known not to carry, from the
// probe of the source and the container to be written. It is consulted before
// anything runs, purely to skip an attempt that would fail.
//
// A pair missing from the tables below is not a promise that the copy works. It
// means castor has not been told otherwise, and will find out the way anyone finds
// out about an unmeasured codec: the cast fails with ffmpeg's message in the log,
// and the pair becomes a row.
func Known(probe media.ProbeInfo, into media.FormatInfo) Axes {
	return Axes{
		Video: refuses(videoRules, probe.VideoCodec, probe, into),
		Audio: refuses(audioRules, probe.AudioCodec, probe, into),
	}
}

// rule is one measured pair a container will not carry.
type rule struct {
	// why is for the log, so a re-encode chosen because the container refused the
	// codec is distinguishable from one chosen because the renderer cannot decode it.
	why string
	// when reports whether the rule applies to this codec, probe and destination.
	when func(codec media.Codec, probe media.ProbeInfo, into media.FormatInfo) bool
}

func refuses(rules []rule, codec media.Codec, probe media.ProbeInfo, into media.FormatInfo) bool {
	if codec == "" {
		return false // no track, nothing to carry
	}
	return slices.ContainsFunc(rules, func(r rule) bool { return r.when(codec, probe, into) })
}

// inBand and outOfBand match the destination's declared framing. Neither matches
// a container that declared none, so a FormatInfo built anywhere but the registry
// is refused outright one layer up rather than silently answered here.
func inBand(into media.FormatInfo) bool    { return into.Framing == media.FramingInBand }
func outOfBand(into media.FormatInfo) bool { return into.Framing == media.FramingOutOfBand }

var audioRules = []rule{{
	// The MPEG-TS muxer never refuses a codec. Handed FLAC, Vorbis or raw PCM it
	// exits cleanly, reports a plausible number of bytes muxed, and writes the
	// track as private data with no descriptor, so the output has no audio stream
	// at all. No flag, strictness level or bitstream filter changes that.
	why: "the container has no stream type for this codec and would write it as unreadable private data",
	when: func(c media.Codec, _ media.ProbeInfo, into media.FormatInfo) bool {
		return inBand(into) && (c == media.CodecFLAC || c == media.CodecVorbis || media.IsPCM(c))
	},
}, {
	// The mp4 family refuses what it cannot carry, loudly and before a single byte.
	// aac_latm has no escape either: ffmpeg ships no LATM to ASC filter, and it
	// survives the MPEG-TS spool unchanged, so the read-once path does not rescue it.
	why: "the container has no tag for this codec and would refuse to write a header",
	when: func(c media.Codec, _ media.ProbeInfo, into media.FormatInfo) bool {
		return outOfBand(into) && slices.Contains(
			[]media.Codec{media.CodecAACLATM, media.CodecWMAv2, "pcm_mulaw", "pcm_alaw"}, c)
	},
}, {
	// The one rule here that no artifact can reveal. TrueHD copied into a
	// fragmented mp4 on a pipe with no video mapped exits 0 and produces packets
	// that do not decode, while the same command with a video track mapped is
	// correct: the non-seekable output carries extra trun offset bytes before mdat,
	// so the demuxer slices samples at the wrong offset. It prints nothing at all,
	// so it has to be known rather than seen.
	why: "TrueHD in a fragmented mp4 on a pipe is only sliced correctly when a video track is mapped alongside it",
	when: func(c media.Codec, probe media.ProbeInfo, into media.FormatInfo) bool {
		return c == media.CodecTrueHD && into.Muxer == media.MuxerMP4 && probe.VideoCodec == ""
	},
}}

var videoRules = []rule{{
	// MPEG-TS carries four video codecs: H.264, HEVC, MPEG-2 and MPEG-4 part 2.
	// Everything else is written as private data and reads back with no video
	// stream at all, at a clean exit. AV1 is worse than useless, misidentified on
	// read as mpeg4 and yielding a handful of readable packets, so the renderer is
	// handed a video stream that is simply corrupt. msmpeg4v3 is in the broken set
	// while plain mpeg4 is not, which is why the set cannot be guessed from how old
	// a codec is, and why the table below is a hint rather than the answer.
	why: "the container has no stream type for this codec and would write it as unreadable private data",
	when: func(c media.Codec, _ media.ProbeInfo, into media.FormatInfo) bool {
		return inBand(into) && slices.Contains([]media.Codec{
			media.CodecVP8, media.CodecVP9, media.CodecAV1,
			media.CodecMJPEG, media.CodecMSMPEG4v3,
		}, c)
	},
}, {
	// The codecs the mp4 family has no tag for. Every one of these was measured
	// against a real muxer; the list grows by measurement and by bug report, which
	// is the honest cost of a table over a runtime probe.
	why: "the container has no tag for this codec and would refuse to write a header",
	when: func(c media.Codec, _ media.ProbeInfo, into media.FormatInfo) bool {
		return outOfBand(into) && slices.Contains([]media.Codec{
			media.CodecVP8, media.CodecMSMPEG4v3,
			// The older web and broadcast codecs, all of which the mp4 family refuses
			// the same way: no tag, no header, nothing written. They are here because
			// castor now reads containers it used to refuse outright (FLV, raw MPEG-TS,
			// anything ffprobe names), so these reach the muxer where they never used to.
			"theora", "flv1", "vp6f", "wmv1", "wmv2", "wmv3", "vc1",
			"h263", "h263p", "prores", "dvvideo", "rv40", "svq3",
		}, c)
	},
}}

// Reason returns why a container will not carry a track, for the log: a re-encode
// chosen because the container refused the codec looks exactly like one chosen
// because the renderer cannot decode it.
func Reason(probe media.ProbeInfo, into media.FormatInfo) (video, audio string) {
	for _, r := range videoRules {
		if probe.VideoCodec != "" && r.when(probe.VideoCodec, probe, into) {
			video = r.why
			break
		}
	}
	for _, r := range audioRules {
		if probe.AudioCodec != "" && r.when(probe.AudioCodec, probe, into) {
			audio = r.why
			break
		}
	}
	return video, audio
}
