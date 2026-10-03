package ffmpeg

// The names ffmpeg muxes and demuxes a container under (-f), and ffprobe reports it by.
const (
	FormatMPEGTS = "mpegts"
	FormatMP4    = "mp4"
	FormatHLS    = "hls"
	FormatDASH   = "dash"
)

// MPEGTSResetArgs start an MPEG-TS output at its own timestamps, its continuity counters flagged as restarting so a reader drops nothing as corrupt.
var MPEGTSResetArgs = []string{"-mpegts_flags", "+initial_discontinuity", "-muxdelay", "0", "-muxpreload", "0"}
