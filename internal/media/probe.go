package media

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ProbeInfo is what one ffprobe pass establishes about a source: what container it is in, how
// long it runs, how fat it is, and what the two tracks castor cares about are made of.
//
// It is one type for both layers that measure, and it used to be two: a record for ranking a
// candidate and one for deciding a stream copy, overlapping on the height and disagreeing about
// the thing they were both looking at (one required dimensions and a codec that decodes to
// motion, the other took the first stream calling itself video), so a decoy playlist was
// refused as a candidate and, cast by hand, copy-decided as if it carried a picture.
//
// Zero values mean "unknown", and most capability checks read that as "not safe to copy": a
// partial probe falling back to a transcode costs quality rather than the cast. The exception
// is AudioChannels, where an unknown count is trusted rather than force-transcoded (see
// AudioSupport.accepts), since a matching codec with no probed layout is not worth a re-encode.
type ProbeInfo struct {
	// ContentType is the container. Empty means castor has no name for it, which is not a
	// failure: it only chooses input flags and whether a renderer might be handed the URL, and
	// an unnamed container answers both the safe way (see FormatToContentType).
	ContentType string
	// BitRate is the container's own top-level rate, 0 where ffprobe reports none, which is its
	// most common shape for an HLS master.
	BitRate int64
	// Duration is the runtime the container declares, 0 for a live edge and for the many
	// playlists ffprobe reports none for. It is the only runtime a whole file has.
	Duration time.Duration

	VideoCodec    Codec  // e.g. CodecH264, CodecHEVC
	VideoProfile  string // e.g. "High", "Main", "High 10"
	VideoHeight   int
	VideoBitDepth int  // derived from pix_fmt (8, 10, 12)
	VideoHDR      bool // PQ (smpte2084) or HLG (arib-std-b67) transfer

	AudioCodec    Codec // e.g. CodecAAC, CodecAC3
	AudioChannels int   // channel count (2 = stereo, 6 = 5.1, 8 = 7.1), 0 if unknown
}

// Playable reports whether the source carries a castable program: a real picture plus audio.
// Decoy playlists (an image-only "video" track, or video with no audio) probe cleanly, often
// carry the highest bandwidth in a pool, and cannot be remuxed into anything a renderer plays.
func (p ProbeInfo) Playable() bool { return p.VideoCodec != "" && p.AudioCodec != "" }

// ProbeEntries is the -show_entries selection DecodeProbe reads, so what is asked for and
// what is decoded cannot drift apart.
//
// extradata_size is deliberately absent, though it would work: nine source shapes agreed nine
// times out of nine that extradata_size and the 0xFFF ADTS syncword identify a track's
// framing, so castor COULD measure an input's framing here. No decision needs it, because the
// repack is keyed on the destination and is a no-op in the harmless direction, and adding an
// input nothing reads is how a decision later grows a dependency on the source container by
// accident.
const ProbeEntries = "format=format_name,bit_rate,duration:" +
	"stream=codec_type,codec_name,profile,width,height,pix_fmt,color_transfer,channels"

// DecodeProbe maps one ffprobe JSON document to the facts both layers read. It runs no
// subprocess: the adapters own the binary, the flags each input needs to be opened at all and
// the budget a measurement gets, and this owns what the answer MEANS.
//
// A document with no format name is an error rather than an empty answer: ffprobe writes one
// for every input it opened at all, so its absence says the JSON is not a measurement.
func DecodeProbe(out []byte) (ProbeInfo, error) {
	var result struct {
		Streams []struct {
			CodecType     string `json:"codec_type"`
			CodecName     string `json:"codec_name"`
			Profile       string `json:"profile"`
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			PixFmt        string `json:"pix_fmt"`
			ColorTransfer string `json:"color_transfer"`
			Channels      int    `json:"channels"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			BitRate    string `json:"bit_rate"`
			Duration   string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return ProbeInfo{}, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	if result.Format.FormatName == "" {
		return ProbeInfo{}, fmt.Errorf("ffprobe returned no format name")
	}

	info := ProbeInfo{ContentType: FormatToContentType(result.Format.FormatName)}
	// A non-numeric rate is left at zero rather than refused: it is the weakest signal
	// castor ranks on and no decision turns on it alone.
	info.BitRate, _ = strconv.ParseInt(result.Format.BitRate, 10, 64)
	// Fractional seconds ("5405.400000"), or absent/"N/A" for a live edge. Unparseable
	// stays zero, which every reader takes as "unknown", never as "short".
	if secs, err := strconv.ParseFloat(result.Format.Duration, 64); err == nil && secs > 0 {
		info.Duration = time.Duration(secs * float64(time.Second))
	}

	for _, s := range result.Streams {
		switch s.CodecType {
		case "video":
			// A real picture has dimensions and a codec that decodes to motion, so the first
			// stream calling itself video is not the answer: the first REAL one is, and later
			// thumbnails are ignored.
			if info.VideoCodec != "" || s.Width <= 0 || s.Height <= 0 || imageCodecs[s.CodecName] {
				continue
			}
			info.VideoCodec = Codec(s.CodecName)
			info.VideoProfile = s.Profile
			info.VideoHeight = s.Height
			info.VideoBitDepth = pixFmtBitDepth(s.PixFmt)
			info.VideoHDR = isHDRTransfer(s.ColorTransfer)
		case "audio":
			// The first audio track is the default a pull maps as 0:a:0; later alternates and
			// commentary are not what will be read.
			if info.AudioCodec != "" {
				continue
			}
			info.AudioCodec = Codec(s.CodecName)
			info.AudioChannels = s.Channels
		}
	}
	return info, nil
}

// imageCodecs are ffmpeg codec names that decode to a still image rather than motion. A
// source whose only "video" track is one of these is a decoy an aggregator serves: it probes
// cleanly, and nothing a renderer can play comes out of it.
var imageCodecs = map[string]bool{
	"png": true, "apng": true, "mjpeg": true, "jpeg": true, "jpegls": true,
	"bmp": true, "gif": true, "tiff": true, "webp": true, "ppm": true,
}

// pixFmtBitDepth derives the luma bit depth from an ffprobe pix_fmt name. 8-bit formats
// (yuv420p, nv12, yuvj420p) carry no depth marker; 10/12-bit ones do (yuv420p10le, p010le,
// yuv422p12le).
func pixFmtBitDepth(pixFmt string) int {
	switch {
	case pixFmt == "":
		return 0
	case strings.Contains(pixFmt, "12"):
		return 12
	case strings.Contains(pixFmt, "10"):
		return 10
	default:
		return 8
	}
}

// isHDRTransfer reports whether an ffprobe color_transfer names an HDR curve.
func isHDRTransfer(transfer string) bool {
	switch transfer {
	case "smpte2084", "arib-std-b67":
		return true
	default:
		return false
	}
}

// A count of unreadable ("data") streams is deliberately not a field here. A refused track
// is detected by its ABSENCE from the artifact, not by the presence of something else: a
// container with no stream type for a codec writes it as private data, but so do containers
// carrying genuine timed metadata, so a count would convict a healthy source as often as a
// destroyed one (see the carriage tables, which answer from the pair instead).
