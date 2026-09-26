package ffmpeg

// The names ffmpeg muxes and demuxes a container under (-f), and ffprobe reports it by.
const (
	FormatMPEGTS = "mpegts"
	FormatMP4    = "mp4"
	FormatHLS    = "hls"
	FormatDASH   = "dash"
)
