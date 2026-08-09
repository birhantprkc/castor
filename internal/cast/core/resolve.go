package core

import (
	"context"
	"log/slog"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// VideoPolicy answers exactly one question: how far does this leg trust the envelope
// the renderer advertised. Being permissive there is right, because refusing a copy over
// a profile a device probably decodes buys a whole title of needless transcode, and the
// two served shapes differ in how much that gamble costs them: the read-once spool is
// produced for one renderer that has already answered, while a remux changes the wrapper
// and not the picture, so a bitstream it copies is the bitstream the source published.
//
// It answers NOTHING ELSE, and the ceiling is the boundary that proves it. A user
// stating what they want cast is not an opinion about a device, so no policy value may
// short-circuit it: it used to, and resolver.max_height then meant one thing on a
// buffered cast and nothing at all on a remux, decided by which delivery path a device
// happened to land on and reported nowhere. The same is true of an HDR bitstream, which
// is refused for every renderer alike, and of carriage, which is a fact about the muxer
// rather than a capability gate and whose only effect is to route a doomed copy to the
// ladder.
type VideoPolicy int

const (
	// CopyWhatFits copies only a bitstream in an envelope the renderer advertised, and
	// with no cues to draw over it.
	CopyWhatFits VideoPolicy = iota + 1
	// CopyWhatever copies a bitstream the renderer never advertised, because this leg
	// changes the container and not the picture: the source published this envelope for
	// players in general, and a device that lists h264 and nothing else routinely decodes
	// the hevc a re-encode would have cost the whole title's quality to avoid.
	CopyWhatever
)

// VideoInputs is everything the video decision reads. Every field is a value
// someone else produced; the only I/O is proving an encoder exists on this host.
type VideoInputs struct {
	// Caps is the connected renderer's advertised support.
	Caps media.Renderer
	// Probe measures the MAPPED tracks the encode will read: the local spool on
	// the read-once path, the upstream on a network remux.
	Probe media.ProbeInfo
	// Into is the container this encode writes.
	Into media.FormatInfo
	// Policy is how far this leg trusts the envelope the renderer advertised. Only
	// CopyWhatever lifts that gate, so a leg that forgot to say gets the conservative
	// answer rather than a copy nobody asked for. It lifts nothing else.
	Policy VideoPolicy
	// Decode is the axes a previous attempt of this cast proved must not be copied,
	// because the reader that was copying them exited on the bitstream it was handed
	// (see read's segment-fragile row). It overrules the policy, which is the point: a
	// CopyWhatever leg copies whatever the source is precisely because it has no
	// evidence against the bitstream, and this is that evidence. Zero is the ordinary
	// case, a first attempt with nothing against it.
	Decode carriage.Axes
	// MaxHeight is the cast's height ceiling, the user's instruction rather than anything
	// measured or negotiated. It bounds the copy and scales the re-encode on EVERY leg that
	// produces bytes, whatever that leg's policy: a ceiling one delivery path honoured and
	// another ignored is a ceiling nobody can read off their own configuration, since the
	// path is chosen by what a renderer answered during discovery and no log line named it.
	// The shape castor cannot downscale is refused rather than exempted, which is the
	// composition's half of this same statement (see Shape.Passthrough); what the number
	// means, and why it is not a device capability, is media.HeightCap's.
	MaxHeight media.HeightCap
	// GOPSeconds caps a re-encoded GOP so a renderer joining mid-stream resyncs
	// within it, 0 for the encoder's own default.
	GOPSeconds int
	// BurnIn is the live cue file to draw into every frame, "" for none. Passing
	// it here rather than setting it on the encode afterwards is what makes "a
	// burn-in forces a re-encode" a property of this function instead of an
	// ordering contract between two statements at the call site.
	BurnIn string

	// FFmpegPath is the binary the encoder proof runs.
	FFmpegPath string
}

// DecideVideo is the whole copy-vs-encode answer for the video axis, for both
// served shapes:
//
//   - copy: nothing forces an encode. Four clauses have to hold, and only one of
//     them is the policy's to lift. The cast's height ceiling holds on every
//     policy, because it is what the user asked for rather than a judgement about
//     the renderer. The source's dynamic range holds on every policy for the
//     mirror reason: an HDR bitstream is refused for every renderer alike, because
//     nothing establishes that an arbitrary set engages HDR on a stream it was
//     handed. The output container's carriage holds on every policy too, and
//     it is not a capability gate either: it asks whether ffmpeg's muxer has a
//     stream type for the codec, and its entire effect is to route into the
//     re-encode below a copy the muxer would otherwise have destroyed at a clean
//     exit (the codecs, and what each of them does instead of refusing, are the
//     carriage tables' to state). The fourth clause, the envelope the renderer
//     advertised, is the one CopyWhatever trusts past;
//   - re-encode: otherwise, to the most efficient codec the renderer advertises
//     and this host can hardware-encode (HEVC at half the bitrate, else H.264),
//     bounded by that codec's VBV-capped target, scaled to the cast's ceiling, and
//     carrying the GOP bound and burn-in this leg asked for. The ceiling is the
//     cast's and reaches both branches; the GOP bound is the encode's own, and a
//     copy has nowhere to carry one.
//
// It returns the track rather than writing into an encode, so a caller holds one value
// describing the whole video axis instead of assembling an encode from two functions that
// each knew half of it.
//
// SelectEncoder proves an encoder with a real test encode (cached per process),
// so this takes ctx: a slow or wedged probe is cancelled when the cast's context
// ends rather than running detached.
func DecideVideo(ctx context.Context, in VideoInputs) ffmpeg.VideoTrack {
	blocked := carriage.Known(in.Probe, in.Into).Video
	// The ceiling and the dynamic range are conjoined with the policy's question, never
	// disjuncts of it: as a disjunct CopyWhatever answers true before either is read at all,
	// and a leg that silently stops honouring one is the drift media.HeightCap exists to
	// prevent. Neither was ever a judgement about the renderer, which is why neither sits in
	// the envelope a renderer declares: the ceiling is what the user asked for, and HDR is a
	// policy castor holds for every set alike, correct HDR playback not being something that
	// can be assumed to engage on an arbitrary one. It used to live inside VideoSupport, so
	// on the leg that trusts a renderer past its envelope it was never asked at all and an
	// HDR master was handed over untouched.
	tall := !in.MaxHeight.Admits(in.Probe.VideoHeight)
	fits := (in.Policy == CopyWhatever || in.Caps.CanCopyVideo(in.Probe)) && !tall && !in.Probe.VideoHDR
	if in.BurnIn == "" && !blocked && !in.Decode.Video && fits {
		return ffmpeg.CopyVideo()
	}
	if in.Decode.Video {
		// Kept apart from the container's refusal because they are different failures with
		// different lifetimes: one is a pair the tables know about, the other is a bitstream
		// that already killed a reader of this very cast, and a user reading "the container
		// will not carry this" about a copy that was carriable a minute ago would be reading
		// a lie.
		slog.InfoContext(ctx, "a previous attempt's copy of this video track broke upstream; decoding it instead",
			"codec", string(in.Probe.VideoCodec))
	}
	if blocked {
		// Reason answers only for pairs the tables already knew about, so a refusal
		// that came from the artifact logs an empty reason: the muxer having produced
		// nothing for the track is the whole reason, and there is no second one.
		why, _ := carriage.Reason(in.Probe, in.Into)
		slog.InfoContext(ctx, "the output container will not carry this video track; re-encoding",
			"codec", string(in.Probe.VideoCodec),
			"container", in.Into.ContentType,
			"reason", why)
	}
	enc := selectVideoEncoder(in.Caps, func(c media.Codec) (ffmpeg.Encoder, bool) {
		return ffmpeg.SelectEncoder(ctx, in.FFmpegPath, c)
	})
	if tall {
		// The one cost the ceiling imposes, stated where a user can attribute it, and it is
		// stated after the encoder is resolved because the encoder is half the answer: a decode,
		// scale and re-encode of a 2160p source has to hold realtime for the whole title, which a
		// hardware encoder does comfortably and a software one on a busy host does not, well down
		// into the range watch.Health's calibration convicts a link on. hardware=false beside
		// source_height=2160 is the line that says a stalling
		// cast is castor's own encode and not the link, which nothing downstream can say for it:
		// no deliverability verdict is reached on an encode castor chose to run (see
		// pipeline's pull.judgedPace, and the remux leg, which supplies no pace at all).
		slog.InfoContext(ctx, "the cast's height ceiling forces a transcode of this video track",
			"source_height", in.Probe.VideoHeight,
			"max_height", in.MaxHeight,
			"encoder", enc.Name,
			"hardware", enc.Hardware)
	}
	t := videoTargets[enc.Codec]
	return ffmpeg.EncodeVideo(ffmpeg.VideoEncode{
		Encoder:             enc,
		Bitrate:             t.bitrate,
		Maxrate:             t.maxrate,
		Bufsize:             t.bufsize,
		MaxHeight:           in.MaxHeight,
		KeyframeIntervalSec: in.GOPSeconds,
		SubtitleTextFile:    in.BurnIn,
	})
}

// codecPreference ranks re-encode target codecs by efficiency, most efficient
// first. selectVideoEncoder picks the first one the renderer decodes and this
// host can hardware-encode; H.264 is last and always resolves to at least a
// software baseline, so selection never fails. Adding a codec is one entry here.
var codecPreference = []media.Codec{media.CodecHEVC, media.CodecH264}

// selectVideoEncoder chooses the encoder for a re-encode: the most efficient
// codec both the renderer advertises and this host can produce. A codec above
// H.264 is taken only when a hardware encoder backs it, since software HEVC
// cannot hold realtime at 1080p; H.264 is the floor and accepts its software
// baseline. selectEncoder resolves an encoder for a codec (ffmpeg.SelectEncoder
// in production, a fake in tests); its ok is false for a codec with no encoder.
func selectVideoEncoder(caps media.Renderer, selectEncoder func(media.Codec) (ffmpeg.Encoder, bool)) ffmpeg.Encoder {
	for _, codec := range codecPreference {
		if !caps.SupportsCodec(codec) {
			continue
		}
		if enc, ok := selectEncoder(codec); ok && (enc.Hardware || codec == media.CodecH264) {
			return enc
		}
	}
	// The renderer advertised nothing we can encode to (or only a non-H.264
	// codec with no hardware here); fall back to the universal H.264 baseline.
	enc, _ := selectEncoder(media.CodecH264)
	return enc
}

// videoTarget is a VBV-capped bitrate: the average, the peak cap, and the buffer
// window the cap applies over.
type videoTarget struct{ bitrate, maxrate, bufsize string }

// videoTargets is the re-encode target per codec, bounding the transcoder's
// output so it stays within the renderer's decode budget. HEVC needs about half
// of H.264 for the same quality. maxrate == bitrate makes the VBV cap a true
// ceiling rather than an average the encoder overshoots; bufsize is ~2s. They
// reach a command line only through a VideoEncode, so a stream copy has nowhere
// to carry them. Adding a codec the pipeline encodes to is one entry here.
// H.264's row is the floor's own budget, read from where both producers of the floor
// codec read it (see media.FloorVideoBitrate): the read-once pull emits the same
// fallback and must not aim at a different ceiling than the encode downstream of it.
var videoTargets = map[media.Codec]videoTarget{
	media.CodecH264: {bitrate: media.FloorVideoBitrate, maxrate: media.FloorVideoMaxrate, bufsize: media.FloorVideoBufsize},
	media.CodecHEVC: {bitrate: "2M", maxrate: "2M", bufsize: "4M"},
}
