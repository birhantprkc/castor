package media

import (
	"context"
	"slices"
	"time"
)

type ProbeInfo struct {
	ContentType string
	BitRate     int64         // the container's own top-level rate, 0 where ffprobe reports none
	Duration    time.Duration // the declared runtime, 0 for a live edge and where none is reported

	VideoCodec    Codec // e.g. CodecH264, CodecHEVC
	VideoProfile  Profile
	VideoHeight   int
	VideoBitDepth int  // derived from pix_fmt (8, 10, 12)
	VideoHDR      bool // PQ (smpte2084) or HLG (arib-std-b67) transfer

	AudioCodec    Codec // e.g. CodecAAC, CodecAC3
	AudioChannels int   // channel count (2 = stereo, 6 = 5.1, 8 = 7.1), 0 if unknown

	VideoHeights []int
}

func (p ProbeInfo) Clone() ProbeInfo {
	p.VideoHeights = slices.Clone(p.VideoHeights)
	return p
}

// Prober measures the tracks a reader will actually map.
type Prober interface {
	Probe(ctx context.Context) (ProbeInfo, Reach, error)
}

// Reach is how far an attempt to read a source got, as a fact about the ORIGIN rather than about the media.
type Reach int

const (
	ReachUnproven Reach = iota
	// ReachOpened is the origin serving the source. Only the link is proved live by it.
	ReachOpened
	// ReachRefused is the origin answering with a final no (401, 403, 404, 410).
	ReachRefused
)

func (r Reach) String() string {
	switch r {
	case ReachOpened:
		return "opened"
	case ReachRefused:
		return "refused"
	default:
		return "unproven"
	}
}
