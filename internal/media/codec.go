package media

import "strings"

// Codec is an ffmpeg canonical codec name: the value ffprobe reports for a
// stream and the vocabulary the planner, renderer capabilities, and encoders
// all share, so codecs travel as a type instead of bare strings. It names the
// abstract codec (H.264, AC-3), not a concrete encoder; one codec can have
// several encoders (libx264, h264_vaapi, h264_videotoolbox). The type spans
// both video and audio; VideoSupport / AudioSupport disambiguate which side a
// value belongs to.
type Codec string

// Video codecs.
const (
	CodecH264 Codec = "h264"
	CodecHEVC Codec = "hevc"
)

// Audio codecs. AC-3 and E-AC-3 are the Dolby surround codecs: unlike AAC (which
// a renderer commonly decodes stereo-only), advertised support for either means
// the renderer decodes multichannel, so a 5.1 source can reach it intact.
const (
	CodecAAC  Codec = "aac"
	CodecAC3  Codec = "ac3"
	CodecEAC3 Codec = "eac3"
)

// Codecs castor never targets and no renderer advertises, but that arrive from
// sources in the wild. Some carry a copy adaptation rule (see the ffmpeg
// package's tables); the rest are here precisely because they carry none, and the
// copy matrix test names them to pin that absence as deliberate rather than
// overlooked. Naming them keeps every codec in the program one type: a bare
// string in a table would compare equal to a media.Codec by accident and never by
// design.
//
// CodecAACLATM is worth singling out. LOAS/LATM AAC is routine in broadcast and
// some IPTV HLS, it probes as a codec distinct from aac, and it cannot be copied
// into MP4 or HLS-fMP4 at all: exit 234, "Could not find tag for codec aac_latm
// in stream #0", and ffmpeg 8.1.2 ships no LATM to ASC bitstream filter (the
// complete audio bsf set is aac_adtstoasc, dts2pts, eac3_core, extract_extradata,
// opus_metadata, pcm_rechunk, remove_extra, setts, truehd_core, and only the
// first repacks framing). A decision keyed on "is it AAC-ish" would hand it
// aac_adtstoasc, which refuses it at init; keying on the exact probed name
// separates it naturally.
const (
	CodecAACLATM Codec = "aac_latm"
	CodecTrueHD  Codec = "truehd"
	CodecDTS     Codec = "dts"
	CodecFLAC    Codec = "flac"
	CodecVorbis  Codec = "vorbis"
	CodecOpus    Codec = "opus"
	CodecMP3     Codec = "mp3"
	CodecMP2     Codec = "mp2"
	CodecWMAv2   Codec = "wmav2"

	CodecVP8       Codec = "vp8"
	CodecVP9       Codec = "vp9"
	CodecAV1       Codec = "av1"
	CodecMJPEG     Codec = "mjpeg"
	CodecMSMPEG4v3 Codec = "msmpeg4v3"
	CodecMPEG4     Codec = "mpeg4"
	CodecMPEG2     Codec = "mpeg2video"
)

// IsPCM reports whether c is one of ffmpeg's raw PCM codecs (pcm_s16le,
// pcm_mulaw, pcm_f32be and the rest of the family). They are matched by prefix
// rather than enumerated because the family is large, open and uniform for
// castor's purposes: every one of them lands in an MPEG-TS output as a
// private data stream, and pcm_rechunk (the only PCM bitstream filter that
// exists) only regroups packet sizes and cannot give MPEG-TS a stream type it
// does not have.
func IsPCM(c Codec) bool { return strings.HasPrefix(string(c), "pcm_") }

// There is deliberately no codec-to-axis classifier here. It would only be
// needed by a caller holding a bare codec name and no idea which half of the
// program it came from, and castor has none: every place that acts on an axis
// (the copy adaptations, the carriage rules, the decision layer) reads
// ProbeInfo.VideoCodec or ProbeInfo.AudioCodec and therefore knows its axis
// statically. A runtime classifier would have to enumerate every codec in the
// wild to answer, and would answer "unknown" for the ones that matter most.

// The re-encode floor: the codecs and the stereo target every renderer castor
// targets decodes and every container it produces carries. Two places emit it,
// the decision layer when no better option survives and the read-once pull when
// the spool container refuses a copy, and they have to agree: a pull that fell
// back to a different bitrate than the encode downstream would spend quality
// twice for no reason.
const (
	FloorVideoCodec    = CodecH264
	FloorAudioCodec    = CodecAAC
	FloorAudioBitrate  = "256k"
	FloorAudioChannels = 2

	// FloorVideoBitrate, FloorVideoMaxrate and FloorVideoBufsize are the rate control
	// the floor codec is produced under: the average, the peak the encoder may never
	// exceed, and the window that peak is measured over (~2s at the cap). maxrate equal
	// to bitrate is what makes the VBV cap a ceiling rather than an average the encoder
	// overshoots.
	//
	// They are here for the same reason the codec is: both producers of the floor have
	// to agree, and one of them had no ceiling at all. The read-once pull's fallback was
	// -crf 23 at the source's own resolution, the only rate-control authority in castor
	// with nothing bounding it, so on a 3840x2160 source the recovery for a copy that
	// broke asked a software encoder for tens of Mbit/s in realtime, produced something
	// heavier than what it was escaping, and fell behind playback: a cast convicted as
	// undeliverable for castor's own arithmetic. Capped, the same fallback targets the
	// budget the decision layer has always given H.264.
	FloorVideoBitrate = "4M"
	FloorVideoMaxrate = "4M"
	FloorVideoBufsize = "8M"
)
