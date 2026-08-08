package core

import (
	"context"
	"log/slog"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// SubtitleForServed is the subtitle axis of a cast castor produces the picture for.
// Config is its only input, and that is a property of where it is asked: only the
// read-once composition draws cues, and that composition is chosen exactly when the
// renderer never fetches for itself, so the clause a general rule would add (there must be
// a local drawtext encode to draw into) is already true of every cast that reaches here.
// A renderer that fetches for itself either takes the source URL or is served a remux, and
// neither has decoded frames to draw on; it takes captions as a native track instead.
//
// It takes config rather than a renderer because the answer is needed before one has
// answered anything: the read must know whether to tee PCM the moment it starts, which is
// long before there are capabilities to read. The only way to ask a renderer-shaped
// question that early was to hand the planner a fabricated media.Renderer, a capability
// record no device produced, standing in for one that had not connected yet.
func SubtitleForServed(cfg Config) SubtitleMode {
	if cfg.Whisper.Enable {
		return SubtitleBurnIn
	}
	return SubtitleOff
}

// VideoPolicy is how much the video copy decision is allowed to refuse. The two
// served shapes differ here and nowhere else: the read-once spool re-encodes for
// a height ceiling, an envelope the renderer cannot decode, or a burn-in, while
// the network remux copies whatever the source is because it changes the wrapper
// and not the picture. Carriage applies to both: it is a fact about the muxer,
// not a capability gate, and its only effect is to route a doomed copy to the
// ladder.
type VideoPolicy int

const (
	// CopyWhatFits copies only a bitstream that needs nothing done to it: within
	// the height ceiling, in an envelope the renderer decodes, no cues to draw.
	CopyWhatFits VideoPolicy = iota + 1
	// CopyWhatever copies the source bitstream whatever it is, because this leg
	// changes the container and not the picture.
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
	// Policy is how much this leg's copy is allowed to refuse. Only CopyWhatever
	// lifts the gates, so a leg that forgot to say gets the conservative answer
	// rather than a copy nobody asked for.
	Policy VideoPolicy
	// Decode is the axes a previous attempt of this cast proved must not be copied,
	// because the reader that was copying them exited on the bitstream (exit 183,
	// "Invalid NAL unit size"). It overrules the policy, which is the point: a
	// CopyWhatever leg copies whatever the source is precisely because it has no
	// evidence against the bitstream, and this is that evidence. Zero is the ordinary
	// case, a first attempt with nothing against it.
	Decode carriage.Axes
	// MaxHeight is the cast's height ceiling, 0 for none. It bounds a copy under
	// CopyWhatFits and scales a re-encode on either policy.
	MaxHeight int
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
//   - copy: nothing forces an encode. Under CopyWhatFits that means no cues to
//     burn, the source under the height ceiling and an envelope the renderer
//     decodes; under CopyWhatever it means only that the container can carry the
//     bitstream. That last clause is not a capability gate: it asks whether
//     ffmpeg's muxer has a stream type for the codec, and its entire effect is to
//     route into the re-encode below a copy the muxer would otherwise have
//     destroyed. -f mpegts writes VP8, VP9, AV1, MJPEG and msmpeg4v3 as private
//     data at exit 0 with hundreds of KB written and no video stream at all in the
//     output (av1 is worse: it reads back misidentified as mpeg4 with 7 of 150
//     packets readable, so the renderer is handed a video stream, just a corrupt
//     one). Re-encoding into the identical destination flag set gave exit 0 and
//     150/150 decoded frames for every impossible cell;
//   - re-encode: otherwise, to the most efficient codec the renderer advertises
//     and this host can hardware-encode (HEVC at half the bitrate, else H.264),
//     bounded by that codec's VBV-capped target and carrying this leg's ceiling,
//     GOP bound and burn-in.
//
// It returns the track rather than writing into an encode, which is what collapses
// the old ResolveVideo/ReencodeVideo pair into one function: a caller holding a
// that belong to it.
//
// SelectEncoder proves an encoder with a real test encode (cached per process),
// so this takes ctx: a slow or wedged probe is cancelled when the cast's context
// ends rather than running detached.
func DecideVideo(ctx context.Context, in VideoInputs) ffmpeg.VideoTrack {
	blocked := carriage.Known(in.Probe, in.Into).Video
	fits := in.Policy == CopyWhatever ||
		(withinMaxHeight(in.Probe, in.MaxHeight) && in.Caps.CanCopyVideo(in.Probe))
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

// withinMaxHeight reports whether a probed source fits under the configured cast
// height ceiling. An unknown height (0) passes: the source is trusted rather than
// force-transcoded on missing metadata. maxHeight 0 also passes, meaning "no
// ceiling", matching every other zero convention in the capability model
// (VideoSupport.Profiles nil is any profile, AudioSupport.MaxChannels 0 is no
// ceiling). Read the other way round it meant "reject every source whose height
// is known", which only config validation was keeping out of production. A source
// above a real cap is not copy-eligible, so it falls through to a transcode that
// scales it down.
func withinMaxHeight(src media.ProbeInfo, maxHeight int) bool {
	return maxHeight == 0 || src.VideoHeight == 0 || src.VideoHeight <= maxHeight
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
