package ffmpeg

import (
	"slices"

	"github.com/stupside/castor/internal/media"
)

// Stream-copy adaptation: what a track needs so the destination container will
// accept it. A copy is not a no-op, because containers disagree about how a
// bitstream carries its decoder configuration, and ffmpeg resolves some of those
// disagreements itself while leaving others to the caller.
//
// Behaviour is a set of small independent adaptations, each declaring what it
// applies to and what it contributes, summed by planCopy. A lookup from codec to
// one answer would not hold: the contributions are different kinds of thing (a
// bitstream filter, a movflags token, a free-standing output option), and more
// than one can apply to the same track. Adding a newly found quirk is one new
// value in a table, never a new branch.
//
// Everything here is about making a copy WORK. Whether a copy can work at all is a
// different question with a different owner: the carriage package, which is asked
// before these adaptations are, and whose answer routes a track to the re-encode
// ladder instead. Keeping the two apart is what stopped this file from being both
// a list of fixes and a list of things that cannot be fixed.

// copySubject is everything an adaptation may look at. There is deliberately no
// source container, URL, file extension or content type here: an HLS playlist
// delivers ADTS AAC when its segments are MPEG-TS and out-of-band AAC when they
// are fMP4, and ffprobe reports format_name=hls for both, so the source cannot
// answer the question. Keying on the destination instead is right either way,
// because a repack is a no-op on a bitstream already in the target framing.
type copySubject struct {
	// Codec is the codec the muxer will be handed for the track this adaptation
	// speaks about: the probed codec of the mapped track on a copy, the codec
	// castor chose to produce on a re-encode. A muxer's rules are about the
	// bitstream it receives and not about who produced it, so an E-AC-3 track
	// castor encoded needs the same header handling as one it copied.
	//
	// "Mapped" is load-bearing. castor pins -map 0:v:0 -map 0:a:0 so the track the
	// planner probed is the track the encoder writes; ffmpeg's own default stream
	// selection would pick the highest-channel-count audio instead, and a repack
	// decided from the wrong track fails at filter init.
	Codec media.Codec

	// Probe is the whole measurement of the mapped pair, for an adaptation whose
	// condition is not about its own track. One row uses it: TrueHD into a
	// fragmented mp4 on a pipe is broken when no video is mapped and correct when
	// one is.
	Probe media.ProbeInfo

	// Into is the container the encode actually writes. On the read-once path that
	// is not the source's container: the MPEG-TS spool re-frames everything through
	// it, so an fMP4 source's out-of-band AAC comes back off the spool as ADTS and
	// needs a repack a direct remux of the same source would not.
	Into media.FormatInfo

	// Copying distinguishes a bitstream passing through untouched from one castor
	// is producing. Only rows about inherited framing may read it, since a
	// re-encode emits whatever framing its muxer asks for. Rows about the muxer's
	// own header writing must not, because the muxer behaves the same either way.
	Copying bool
}

// copyAdaptation is one rule about handing a codec to a container, plus what
// obeying it costs. Every field is a contribution that composes: two adaptations
// firing on one track chain their Filters, merge their MovFlags and concatenate
// their OutputArgs.
type copyAdaptation struct {
	// Name is a short slug, logged next to the copy it applied to.
	Name string

	// When reports whether this adaptation applies. Built from the combinators
	// below rather than written inline, so a row reads as a sentence.
	When func(copySubject) bool

	// Filters are bitstream filters contributed to this track's -bsf chain, joined
	// with "," in table order. A filter handed a codec it does not support aborts
	// the encode before anything is written, so any row with Filters must have a
	// When that pins the codec.
	Filters []string

	// MovFlags are +tokens merged into the mp4 family's -movflags value. They
	// cannot be emitted as a second -movflags, which is an AVOption where the last
	// one wins and would clobber the base.
	MovFlags []string

	// OutputArgs are free-standing output options, emitted after the codec options
	// and before -f. Placement matters: given before -i, -strict -2 never reaches
	// the muxer.
	OutputArgs []string
}

// copyPlan is the sum of every adaptation that applied to one track.
type copyPlan struct {
	// Filters is the -bsf chain, in table order, or nil.
	Filters []string
	// MovFlags are the extra -movflags tokens, or nil.
	MovFlags []string
	// OutputArgs are the extra output options, or nil.
	OutputArgs []string
	// Applied names every adaptation that fired, for the decision log.
	Applied []string
}

// anyOf builds a membership test over a fixed set. It is generic because the
// predicates test two comparable vocabularies with the same shape, codecs and
// ffmpeg muxer names.
func anyOf[T comparable](vals ...T) func(T) bool {
	return func(v T) bool { return slices.Contains(vals, v) }
}

// codecIn matches the codec the muxer will be handed.
func codecIn(codecs ...media.Codec) func(copySubject) bool {
	in := anyOf(codecs...)
	return func(s copySubject) bool { return in(s.Codec) }
}

// framedAs matches the destination container's declared framing. It never matches
// FramingUnknown, so a FormatInfo that did not come from the registry gets no
// adaptation and is refused one layer up.
func framedAs(f media.Framing) func(copySubject) bool {
	return func(s copySubject) bool { return f != media.FramingUnknown && s.Into.Framing == f }
}

// muxedBy matches the destination's ffmpeg muxer by name, for rows whose fact is
// about a specific muxer's implementation rather than a container property any
// other muxer shares. Rows whose fact is a container property use framedAs.
func muxedBy(muxers ...string) func(copySubject) bool {
	in := anyOf(muxers...)
	return func(s copySubject) bool { return s.Into.Muxer != "" && in(s.Into.Muxer) }
}

// all composes predicates conjunctively, the main operator the tables need: every
// rule is an intersection of a codec set and a destination property.
func all(preds ...func(copySubject) bool) func(copySubject) bool {
	return func(s copySubject) bool {
		return !slices.ContainsFunc(preds, func(p func(copySubject) bool) bool { return !p(s) })
	}
}

// copying matches a track passing through untouched. It belongs only on rows
// about inherited framing, never on rows about the muxer's header writing, which
// applies whoever produced the bitstream.
func copying(s copySubject) bool { return s.Copying }

// audioCopyAdaptations is every rule about handing an audio track to a container
// castor produces. A codec absent from it needs nothing anywhere: MP3, MP2, Opus
// and DTS all pass through every destination intact, so an absent codec means
// "nothing to do" rather than "not considered".
var audioCopyAdaptations = []copyAdaptation{
	{
		// AAC is framed with a per-frame ADTS header inside MPEG-TS and as a single
		// AudioSpecificConfig inside MP4. A container that writes its header before
		// it has seen a packet cannot derive the second from the first, and rejects
		// the track outright. The filter is a no-op on AAC already out-of-band, which
		// is why the rule needs to know nothing about the source. It matches aac and
		// not aac_latm, a different codec id the filter refuses.
		Name:    "aac-adts-to-asc",
		When:    all(copying, codecIn(media.CodecAAC), framedAs(media.FramingOutOfBand)),
		Filters: []string{"aac_adtstoasc"},
	},
	{
		// AC-3, E-AC-3 and TrueHD derive their sample description box from the first
		// frame, so an mp4 muxer that has already written its moov refuses to write a
		// header at all. delay_moov holds the header back until every mapped track has
		// produced a packet, which is why it is not applied unconditionally: it
		// withholds every byte, ftyp included, until then. -frag_duration bounds the
		// fragment so that wait is a fraction of a second rather than a whole GOP.
		//
		// It is not gated on copying. The muxer derives that box from the first frame
		// whoever produced it, so a source castor re-encodes to E-AC-3 needs it too,
		// and that is the rung core.DecideAudio climbs for any multichannel source
		// the renderer cannot decode. The hls muxer needs none of this: it writes
		// init.mp4 after the first packet by construction.
		Name:       "delay-moov-for-dolby",
		When:       all(codecIn(media.CodecAC3, media.CodecEAC3, media.CodecTrueHD), muxedBy(media.MuxerMP4)),
		MovFlags:   []string{"delay_moov"},
		OutputArgs: []string{"-frag_duration", "1000000"},
	},
}

// videoCopyAdaptations is every rule about handing a video track to a container.
// It is shorter than the audio table because video needs no framing repack in
// either direction: toward MPEG-TS ffmpeg auto-inserts the right *_mp4toannexb
// per actual codec, and toward the mp4 family movenc converts Annex B in its own
// code path and synthesises avcC. castor's +empty_moov does disable ffmpeg's
// automatic bitstream filtering, which is exactly why AAC needs a hand-written
// filter, but video survives it because its conversion is native to the muxer.
//
// Deliberately absent: 10-bit and HDR. Both copy cleanly into every destination
// with pix_fmt and colour signalling preserved, because that signalling rides in
// the bitstream where a copy never touches it. Whether a renderer engages HDR is
// a capability question and never a mux one.
var videoCopyAdaptations = []copyAdaptation{
	{
		// An HEVC track lands in an mp4 tagged hev1, meaning parameter sets in band
		// only, while Apple and Chromecast fMP4 players conventionally require hvc1.
		// This is the one row justified by convention rather than by a mux failure,
		// since the copy succeeds either way. It is codec-gated because the tag is
		// fatal on anything else: H.264 plus -tag:v hvc1 writes no header at all.
		Name:       "hevc-hvc1-tag",
		When:       all(codecIn(media.CodecHEVC), framedAs(media.FramingOutOfBand)),
		OutputArgs: []string{"-tag:v", "hvc1"},
	},
}

// planAudioCopy sums every audio adaptation that applies to a copied audio track.
func planAudioCopy(probe media.ProbeInfo, into media.FormatInfo) copyPlan {
	return planCopy(audioCopyAdaptations, copySubject{Codec: probe.AudioCodec, Probe: probe, Into: into, Copying: true})
}

// planAudioEncode sums every audio adaptation that applies to a track castor is
// producing, where codec is the encoder's output rather than anything the source
// had. A muxer's header rules do not care who made the bitstream, so the same
// table answers both: skipping this call leaves an encoded E-AC-3 track in a
// fragmented mp4 with no header written at all.
func planAudioEncode(codec media.Codec, probe media.ProbeInfo, into media.FormatInfo) copyPlan {
	return planCopy(audioCopyAdaptations, copySubject{Codec: codec, Probe: probe, Into: into})
}

// planVideoCopy sums every video adaptation that applies to a copied video track.
func planVideoCopy(probe media.ProbeInfo, into media.FormatInfo) copyPlan {
	return planCopy(videoCopyAdaptations, copySubject{Codec: probe.VideoCodec, Probe: probe, Into: into, Copying: true})
}

// planVideoEncode is the video counterpart of planAudioEncode: castor re-encodes
// to HEVC whenever a renderer advertises it, and an encoded HEVC track lands in
// an mp4 tagged hev1 exactly like a copied one.
func planVideoEncode(codec media.Codec, probe media.ProbeInfo, into media.FormatInfo) copyPlan {
	return planCopy(videoCopyAdaptations, copySubject{Codec: codec, Probe: probe, Into: into})
}

// planCopy walks the table once and accumulates.
//
// An empty codec, meaning no track or a probe that failed, matches nothing and
// yields the empty plan. That is correct: there is no track to adapt, and the
// optional -map suffix will simply not match one.
//
// The table is walked in declaration order and never sorted, because that order
// is the -bsf chain's order and part of the contract.
func planCopy(table []copyAdaptation, s copySubject) copyPlan {
	var plan copyPlan
	for _, a := range table {
		if !a.When(s) {
			continue
		}
		plan.Filters = append(plan.Filters, a.Filters...)
		plan.MovFlags = append(plan.MovFlags, a.MovFlags...)
		plan.OutputArgs = append(plan.OutputArgs, a.OutputArgs...)
		plan.Applied = append(plan.Applied, a.Name)
	}
	return plan
}
