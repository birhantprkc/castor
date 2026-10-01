package transcode

import (
	"slices"

	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// Stream-copy adaptation: what a track needs so the destination container accepts it.

// copySubject is adaptation input; deliberately no source container (HLS case).
type copySubject struct {
	// codec the muxer receives; "mapped" load-bearing (-map 0:V:0 -map 0:a:0).
	codec media.Codec

	// into: output container; MPEG-TS spool re-frames, may differ from source.
	into container.FormatInfo

	// copying distinguishes pass-through from encode; inherited framing rows only.
	copying bool
}

// copyAdaptation is one codec-to-container rule; adaptations chain/merge on track.
type copyAdaptation struct {
	when func(copySubject) bool

	// filters: bitstream filters joined with "," in table order; must gate on codec.
	filters []string

	// movFlags: +tokens merged into -movflags (not emitted twice, last wins).
	movFlags []string

	// outputArgs: output options after codec opts, before -f (placement matters).
	outputArgs []string
}

// copyPlan is the sum of every adaptation that applied to one track.
type copyPlan struct {
	filters    []string
	movFlags   []string
	outputArgs []string
}

func anyOf[T comparable](vals ...T) func(T) bool {
	return func(v T) bool { return slices.Contains(vals, v) }
}

func codecIn(codecs ...media.Codec) func(copySubject) bool {
	in := anyOf(codecs...)
	return func(s copySubject) bool { return in(s.codec) }
}

// framedAs matches destination framing; never FramingUnknown (registry-only).
func framedAs(f media.Framing) func(copySubject) bool {
	return func(s copySubject) bool { return f != media.FramingUnknown && s.into.Framing == f }
}

// muxedBy matches destination muxer name; use for muxer-specific facts (not properties).
func muxedBy(muxers ...string) func(copySubject) bool {
	in := anyOf(muxers...)
	return func(s copySubject) bool { return s.into.Muxer != "" && in(s.into.Muxer) }
}

func all(preds ...func(copySubject) bool) func(copySubject) bool {
	return func(s copySubject) bool {
		return !slices.ContainsFunc(preds, func(p func(copySubject) bool) bool { return !p(s) })
	}
}

// copying matches pass-through tracks; inherited framing rows only.
func copying(s copySubject) bool { return s.copying }

// audioCopyAdaptations: audio rules; MP3, MP2, Opus, DTS need nothing.
var audioCopyAdaptations = []copyAdaptation{
	{
		// AAC: ADTS inside TS, ASC inside MP4; filter needed for container headers.
		when:    all(copying, codecIn(media.CodecAAC), framedAs(media.FramingOutOfBand)),
		filters: []string{"aac_adtstoasc"},
	},
	{
		// AC-3/EAC-3/TrueHD: delay_moov (unfrag mux until first frame); HLS no-op.
		when:       all(codecIn(media.CodecAC3, media.CodecEAC3, media.CodecTrueHD), muxedBy(ffmpeg.FormatMP4)),
		movFlags:   []string{"delay_moov"},
		outputArgs: []string{"-frag_duration", "1000000"},
	},
}

// videoCopyAdaptations: video rules; 10-bit/HDR omitted (copy cleanly).
var videoCopyAdaptations = []copyAdaptation{
	{
		// HEVC lands tagged hev1; Apple/Chromecast expect hvc1 by convention.
		when:       all(codecIn(media.CodecHEVC), framedAs(media.FramingOutOfBand)),
		outputArgs: []string{"-tag:v", "hvc1"},
	},
}

// planCopy walks table once; empty codec yields empty plan; order is -bsf chain order.
func planCopy(table []copyAdaptation, s copySubject) copyPlan {
	var plan copyPlan
	for _, a := range table {
		if !a.when(s) {
			continue
		}
		plan.filters = append(plan.filters, a.filters...)
		plan.movFlags = append(plan.movFlags, a.movFlags...)
		plan.outputArgs = append(plan.outputArgs, a.outputArgs...)
	}
	return plan
}
