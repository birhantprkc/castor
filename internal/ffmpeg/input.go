package ffmpeg

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/stupside/castor/internal/media"
)

// The command-line fragments ffmpeg and ffprobe both open a source with, so the probe opens it as the reader will.

// AdaptiveInputArgs returns a fresh command fragment for the source manifest type.
func AdaptiveInputArgs(contentType string, segmentRetries int) []string {
	switch contentType {
	case media.HLS:
		// Named, since the demuxer refuses odd names; unseekable, or it walks byte ranges without delivering them.
		return append([]string{"-f", FormatHLS, "-http_seekable", "0"}, hlsTolerances(segmentRetries)...)
	case media.DASH:
		return []string{"-f", FormatDASH, "-allowed_extensions", "ALL"}
	default:
		return nil
	}
}

// LenientInputArgs opens a source of unknown format: every media.HLS tolerance, without naming a demuxer; others skip them.
func LenientInputArgs() []string { return hlsTolerances(0) }

func hlsTolerances(segmentRetries int) []string {
	args := []string{
		// A publisher's EXT-X-START decides where a live read joins, not the demuxer's default three segments back.
		"-prefer_x_start", "1",
		"-allowed_extensions", "ALL",
		"-allowed_segment_extensions", "ALL",
		"-extension_picky", "0",
		"-seg_format_options", "extension_picky=0",
	}
	// Only the media.HLS demuxer takes it; others abort on the unknown option.
	if segmentRetries > 0 {
		args = append(args, "-seg_max_retry", strconv.Itoa(segmentRetries))
	}
	return args
}

type ProbeInput struct {
	ID   media.InputID
	URL  string
	Args []string
}

// HeaderArgs renders h as the ffmpeg/ffprobe -headers flag pair, or nil when h is empty.
func HeaderArgs(h http.Header) []string {
	if len(h) == 0 {
		return nil
	}
	var b strings.Builder
	for _, key := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[key] {
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	return []string{"-headers", b.String()}
}
