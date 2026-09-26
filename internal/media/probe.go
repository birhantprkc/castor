package media

import (
	"context"
	"maps"
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
	// VideoLevel is the codec level in ffprobe's units (H.264 x10, so 42 is 4.2), 0 if unknown.
	VideoLevel     int
	VideoFrameRate float64
	// VideoInterlaced is a picture coded as fields, which a renderer that does not deinterlace shows combed.
	VideoInterlaced bool
	// VideoRotation is the display matrix's turn in degrees; no container castor serves carries it.
	VideoRotation int

	AudioCodec      Codec // e.g. CodecAAC, CodecAC3
	AudioChannels   int   // channel count (2 = stereo, 6 = 5.1, 8 = 7.1), 0 if unknown
	AudioSampleRate int   // Hz, 0 if unknown

	VideoHeights []int

	// ProgramHeights is the tallest picture of each program by id, 0 for one without a picture.
	ProgramHeights map[int]int

	// Start is the first timestamp the source carries; InputStarts is each input's, for a program read from several.
	Start       time.Duration
	InputStarts map[InputID]time.Duration
}

// TakeVideo is p with every picture fact taken from m; a new video field is carried here or nowhere.
func (p ProbeInfo) TakeVideo(m ProbeInfo) ProbeInfo {
	p.VideoCodec, p.VideoProfile, p.VideoHeight, p.VideoBitDepth = m.VideoCodec, m.VideoProfile, m.VideoHeight, m.VideoBitDepth
	p.VideoHDR, p.VideoLevel, p.VideoFrameRate = m.VideoHDR, m.VideoLevel, m.VideoFrameRate
	p.VideoInterlaced, p.VideoRotation = m.VideoInterlaced, m.VideoRotation
	p.VideoHeights, p.ProgramHeights = slices.Clone(m.VideoHeights), maps.Clone(m.ProgramHeights)
	return p
}

// TakeAudio is p with every sound fact taken from m; a new audio field is carried here or nowhere.
func (p ProbeInfo) TakeAudio(m ProbeInfo) ProbeInfo {
	p.AudioCodec, p.AudioChannels, p.AudioSampleRate = m.AudioCodec, m.AudioChannels, m.AudioSampleRate
	return p
}

func (p ProbeInfo) Clone() ProbeInfo {
	p.VideoHeights = slices.Clone(p.VideoHeights)
	p.ProgramHeights = maps.Clone(p.ProgramHeights)
	p.InputStarts = maps.Clone(p.InputStarts)
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
