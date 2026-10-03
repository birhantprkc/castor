// Package media is castor's shared vocabulary: programs, probes, codecs and device capabilities.
package media

// The content types castor reads or serves.
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
