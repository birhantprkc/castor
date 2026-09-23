package media

// Codec: ffprobe codec name (abstract, not encoder-specific).
type Codec string

// Video codecs.
const (
	CodecH264 Codec = "h264"
	CodecHEVC Codec = "hevc"
)

// AC-3/EAC-3: renderer multichannel support (AAC usually stereo-only).
const (
	CodecAAC  Codec = "aac"
	CodecAC3  Codec = "ac3"
	CodecEAC3 Codec = "eac3"
)

// Unsupported codecs (never targeted, arrive from sources, no copy rules).
const (
	CodecAACLATM Codec = "aac_latm"
	CodecTrueHD  Codec = "truehd"
	CodecFLAC    Codec = "flac"
	CodecVorbis  Codec = "vorbis"
	CodecMP3     Codec = "mp3"
	CodecWMAv2   Codec = "wmav2"

	CodecVP8       Codec = "vp8"
	CodecVP9       Codec = "vp9"
	CodecAV1       Codec = "av1"
	CodecMJPEG     Codec = "mjpeg"
	CodecMSMPEG4v3 Codec = "msmpeg4v3"
	CodecMPEG4     Codec = "mpeg4"
)

// Profile is a codec profile as ffprobe names it; source and device families spell it only with these.
type Profile string

const (
	ProfileBaseline            Profile = "Baseline"
	ProfileConstrainedBaseline Profile = "Constrained Baseline"
	ProfileMain                Profile = "Main"
	ProfileExtended            Profile = "Extended"
	ProfileHigh                Profile = "High"
	ProfileMain10              Profile = "Main 10"
)
