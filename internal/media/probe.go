package media

// ProbeInfo is a structured description of a source stream, the subset an
// ffprobe pass yields that the planner needs to decide whether the source can
// be stream-copied to a renderer or must be re-encoded. Zero values mean
// "unknown"; most capability checks treat that as "not safe to copy" (a
// failed or partial probe falls back to a transcode), except AudioChannels,
// where an unknown count is trusted rather than force-transcoded (see
// AudioSupport.accepts) since a matching codec with no probed layout isn't
// worth the quality loss of an unnecessary re-encode.
//
// It is produced by the ffmpeg adapter (ffmpeg.Probe) and consumed by a
// Renderer's copy check; the type lives here, in the domain, so neither side
// depends on the other.
type ProbeInfo struct {
	VideoCodec    Codec  // e.g. CodecH264, CodecHEVC
	VideoProfile  string // e.g. "High", "Main", "High 10"
	VideoHeight   int
	VideoBitDepth int  // derived from pix_fmt (8, 10, 12)
	VideoHDR      bool // PQ (smpte2084) or HLG (arib-std-b67) transfer

	AudioCodec    Codec // e.g. CodecAAC, CodecAC3
	AudioChannels int   // channel count (2 = stereo, 6 = 5.1, 8 = 7.1), 0 if unknown

}

// A count of unreadable ("data") streams is deliberately not a field here. A
// refused track is detected by its ABSENCE from the artifact, not by the
// presence of something else: a container with no stream type for a codec writes
// it as private data, but so do containers carrying genuine timed metadata, and
