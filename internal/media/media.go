// Package media is castor's shared vocabulary: programs, probes, codecs and renderer capabilities.
package media

const (
	MP4    = "video/mp4"
	MKV    = "video/x-matroska"
	WebM   = "video/webm"
	AVI    = "video/x-msvideo"
	MOV    = "video/quicktime"
	HLS    = "application/x-mpegURL"
	DASH   = "application/dash+xml"
	MPEGTS = "video/mp2t"
	// FLV is here because ffprobe reports it for real sources: castor cannot produce it, only read it.
	FLV = "video/x-flv"
)

func IsSegmented(contentType string) bool { return contentType == HLS || contentType == DASH }

// HeightCap is the ceiling the operator set on what may reach the renderer.
type HeightCap int

// Admits reports whether a picture of this height may reach the renderer.
func (c HeightCap) Admits(height int) bool { return height == 0 || height <= int(c) }

// Framing is where a container keeps the decoder configuration of the elementary streams it carries.
type Framing int

const (
	// FramingUnknown is the zero value: a container that has not declared how it frames its streams.
	FramingUnknown Framing = iota
	// FramingInBand repeats each track's decoder configuration inside the stream, ahead of every frame.
	FramingInBand
	FramingOutOfBand
)

func (f Framing) String() string {
	switch f {
	case FramingInBand:
		return "in-band"
	case FramingOutOfBand:
		return "out-of-band"
	default:
		return "unknown"
	}
}
