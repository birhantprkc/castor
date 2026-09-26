package hls

import (
	"errors"
	"path/filepath"

	"github.com/stupside/castor/e2e/origin"
)

// SingleFile writes every fMP4 segment into one file, addressed by EXT-X-BYTERANGE behind a ranged EXT-X-MAP.
type SingleFile struct{}

func (SingleFile) Name() string { return "hls-fmp4-single-file" }

func (SingleFile) Supports(l origin.Layout) error {
	if l.Rungs > 1 || (l.Audio && l.Carriage == origin.Separate) || l.SegmentExt != "" {
		return errors.New("hls-fmp4-single-file writes one file: one rendition, audio muxed, and no segments to rename")
	}
	return nil
}

// Package bypasses pack, since single_file takes the segment filename literally.
func (SingleFile) Package(dir string, _ origin.Layout) origin.Output {
	const entry, ext = "stream.m3u8", ".m4s"
	return origin.Output{
		Entry:      entry,
		SegmentExt: ext,
		Types:      map[string]string{".m3u8": playlistType, ext: "video/mp4"},
		Args: []string{"-f", "hls", "-hls_time", "1", "-hls_list_size", "0", "-hls_playlist_type", "vod",
			"-hls_segment_type", "fmp4", "-hls_flags", "single_file",
			"-hls_segment_filename", filepath.Join(dir, "stream"+ext), filepath.Join(dir, entry)},
	}
}
