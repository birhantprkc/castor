package media

// Codec is an ffprobe codec name, abstract rather than encoder-specific.
type Codec string

// The codecs castor encodes to.
const (
	CodecH264 Codec = "h264"
	CodecHEVC Codec = "hevc"

	CodecAAC  Codec = "aac"
	CodecAC3  Codec = "ac3"
	CodecEAC3 Codec = "eac3"
)

// The codecs castor only reads from a source, and copies where a device decodes them.
const (
	CodecTrueHD Codec = "truehd"
	CodecFLAC   Codec = "flac"
	CodecVorbis Codec = "vorbis"
	CodecOpus   Codec = "opus"
	CodecMP3    Codec = "mp3"

	CodecVP8   Codec = "vp8"
	CodecVP9   Codec = "vp9"
	CodecAV1   Codec = "av1"
	CodecMJPEG Codec = "mjpeg"
	CodecMPEG4 Codec = "mpeg4"
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
