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
	// Codec the muxer receives; "mapped" load-bearing (-map 0:V:0 -map 0:a:0).
	Codec media.Codec

	// Into: output container; MPEG-TS spool re-frames, may differ from source.
	Into container.FormatInfo

	// Copying distinguishes pass-through from encode; inherited framing rows only.
	Copying bool
}

// copyAdaptation is one codec-to-container rule; adaptations chain/merge on track.
type copyAdaptation struct {
	When func(copySubject) bool

	// Filters: bitstream filters joined with "," in table order; must gate on codec.
	Filters []string

	// MovFlags: +tokens merged into -movflags (not emitted twice, last wins).
	MovFlags []string

	// OutputArgs: output options after codec opts, before -f (placement matters).
	OutputArgs []string
}

// copyPlan is the sum of every adaptation that applied to one track.
type copyPlan struct {
	Filters    []string
	MovFlags   []string
	OutputArgs []string
}

func anyOf[T comparable](vals ...T) func(T) bool {
	return func(v T) bool { return slices.Contains(vals, v) }
}

func codecIn(codecs ...media.Codec) func(copySubject) bool {
	in := anyOf(codecs...)
	return func(s copySubject) bool { return in(s.Codec) }
}

// framedAs matches destination framing; never FramingUnknown (registry-only).
func framedAs(f media.Framing) func(copySubject) bool {
	return func(s copySubject) bool { return f != media.FramingUnknown && s.Into.Framing == f }
}

// muxedBy matches destination muxer name; use for muxer-specific facts (not properties).
func muxedBy(muxers ...string) func(copySubject) bool {
	in := anyOf(muxers...)
	return func(s copySubject) bool { return s.Into.Muxer != "" && in(s.Into.Muxer) }
}

func all(preds ...func(copySubject) bool) func(copySubject) bool {
	return func(s copySubject) bool {
		return !slices.ContainsFunc(preds, func(p func(copySubject) bool) bool { return !p(s) })
	}
}

// copying matches pass-through tracks; inherited framing rows only.
func copying(s copySubject) bool { return s.Copying }

// audioCopyAdaptations: audio rules; MP3, MP2, Opus, DTS need nothing.
var audioCopyAdaptations = []copyAdaptation{
	{
		// AAC: ADTS inside TS, ASC inside MP4; filter needed for container headers.
		When:    all(copying, codecIn(media.CodecAAC), framedAs(media.FramingOutOfBand)),
		Filters: []string{"aac_adtstoasc"},
	},
	{
		// AC-3/EAC-3/TrueHD: delay_moov (unfrag mux until first frame); HLS no-op.
		When:       all(codecIn(media.CodecAC3, media.CodecEAC3, media.CodecTrueHD), muxedBy(ffmpeg.FormatMP4)),
		MovFlags:   []string{"delay_moov"},
		OutputArgs: []string{"-frag_duration", "1000000"},
	},
}

// videoCopyAdaptations: video rules; 10-bit/HDR omitted (copy cleanly).
var videoCopyAdaptations = []copyAdaptation{
	{
		// HEVC lands tagged hev1; Apple/Chromecast expect hvc1 by convention.
		When:       all(codecIn(media.CodecHEVC), framedAs(media.FramingOutOfBand)),
		OutputArgs: []string{"-tag:v", "hvc1"},
	},
}

// planCopy walks table once; empty codec yields empty plan; order is -bsf chain order.
func planCopy(table []copyAdaptation, s copySubject) copyPlan {
	var plan copyPlan
	for _, a := range table {
		if !a.When(s) {
			continue
		}
		plan.Filters = append(plan.Filters, a.Filters...)
		plan.MovFlags = append(plan.MovFlags, a.MovFlags...)
		plan.OutputArgs = append(plan.OutputArgs, a.OutputArgs...)
	}
	return plan
}
