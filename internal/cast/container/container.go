// Package container owns what castor produces and what containers carry (two halves).
package container

import (
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// HLS output filenames: muxer writes, server serves.
const (
	HLSPlaylistName   = "stream.m3u8"
	hLSInitName       = "init.mp4"
	HLSSegmentPattern = "seg_%05d.m4s"
)

const (
	// hlsSegmentSeconds: target only; copy cuts on keyframe (lower bound, not exact).
	hlsSegmentSeconds = 4
	// hlsListSize is how many segments the rolling playlist keeps on disk.
	hlsListSize = 8
	// hlsWindowSeconds: on-disk window and burst size (prebuffer one window).
	hlsWindowSeconds = hlsSegmentSeconds * hlsListSize
)

// HLSWindow: exported for renderer hand-off (deleted media would cause 404).
const HLSWindow = hlsWindowSeconds * time.Second

// HLSArtifactContentType returns the response type for a generated HLS artifact name.
func HLSArtifactContentType(name string) (string, bool) {
	switch strings.ToLower(path.Ext(name)) {
	case path.Ext(HLSPlaylistName):
		return media.HLS, true
	case path.Ext(HLSSegmentPattern):
		return "video/iso.segment", true
	case path.Ext(hLSInitName):
		return media.MP4, true
	default:
		return "", false
	}
}

// DeliveryKind determines how server hands stream to renderer; per-format data.
type DeliveryKind int

const (
	// DeliverStream: one growing output from byte 0 (MPEG-TS, frag mp4).
	DeliverStream DeliveryKind = iota
	// DeliverSegmented is live playlist plus rolling segments (HLS).
	DeliverSegmented
)

// FormatInfo describes container castor produces: MIME type, extension, muxer, etc.
type FormatInfo struct {
	ContentType string
	Extension   string
	Muxer       string
	Delivery    DeliveryKind
	// Framing: how container keeps decoder config; zero is deliberate (see media.Framing).
	Framing media.Framing
	// Tuning: fixed options (not codec-dependent); on row to avoid silent drops.
	Tuning Tuning
}

// Tuning is the fixed output configuration of one muxer.
type Tuning struct {
	// MovFlags: base -movflags token set; nil for muxers without option.
	MovFlags []string
	// Args are the muxer's fixed output options, emitted after -f <muxer>.
	Args []string
	// Output: muxer target (pipe for stream, playlist for segmented).
	Output string
}

// formatRegistry: castor's producible containers; one row = one format with tuning.
var formatRegistry = map[string]FormatInfo{
	media.MPEGTS: {
		ContentType: media.MPEGTS, Extension: ".ts", Muxer: ffmpeg.FormatMPEGTS, Delivery: DeliverStream, Framing: media.FramingInBand,
		Tuning: Tuning{
			// Offset reset options; dead options removed (may return for segmented TS).
			Args:   []string{"-mpegts_flags", "+initial_discontinuity", "-muxdelay", "0", "-muxpreload", "0"},
			Output: ffmpeg.StdoutPipe,
		},
	},
	media.MP4: {
		ContentType: media.MP4, Extension: ".mp4", Muxer: ffmpeg.FormatMP4, Delivery: DeliverStream, Framing: media.FramingOutOfBand,
		Tuning: Tuning{
			// Pipe needs fragmented mp4; empty_moov disables auto BSF (AAC filter hand-written).
			MovFlags: []string{"frag_keyframe", "empty_moov", "default_base_moof"},
			Output:   ffmpeg.StdoutPipe,
		},
	},
	media.HLS: {
		ContentType: media.HLS, Extension: ".m3u8", Muxer: ffmpeg.FormatHLS, Delivery: DeliverSegmented, Framing: media.FramingOutOfBand,
		Tuning: Tuning{
			// Sliding-window fMP4 tail; unset hls_playlist_type so window rolls.
			Args: []string{
				"-hls_time", strconv.Itoa(hlsSegmentSeconds),
				"-hls_list_size", strconv.Itoa(hlsListSize),
				"-hls_flags", "delete_segments+independent_segments",
				"-hls_segment_type", "fmp4",
				"-hls_fmp4_init_filename", hLSInitName,
				"-hls_segment_filename", HLSSegmentPattern,
			},
			Output: HLSPlaylistName,
		},
	},
}

// FormatForContentType returns the FormatInfo for a content type, ok=false if castor cannot produce it.
func FormatForContentType(ct string) (FormatInfo, bool) {
	f, ok := formatRegistry[ct]
	return f, ok
}
