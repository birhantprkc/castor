// Package hls publishes a stream as a VOD HLS playlist, with TS or fMP4 segments.
package hls

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stupside/castor/e2e/origin"
)

const playlistType = "application/x-mpegURL"

type segments struct {
	ext, mime string
	args      []string
}

// pack writes a lone media playlist, or a master when there are several renditions or audio travels on its own.
func pack(dir string, l origin.Layout, seg segments) origin.Output {
	ext := cmp.Or(l.SegmentExt, seg.ext)
	out := origin.Output{
		SegmentExt: ext,
		Types:      map[string]string{".m3u8": playlistType, ".mp4": "video/mp4", ext: seg.mime},
		Args:       append([]string{"-f", "hls", "-hls_time", "1", "-hls_list_size", "0", "-hls_playlist_type", "vod"}, seg.args...),
	}
	separateAudio := l.Audio && l.Carriage == origin.Separate
	if l.Rungs == 1 && !separateAudio {
		out.Entry = "stream.m3u8"
		out.Args = append(out.Args, "-hls_segment_filename", filepath.Join(dir, "seg_%03d"+ext), filepath.Join(dir, out.Entry))
		return out
	}
	out.Entry = "master.m3u8"
	out.Args = append(out.Args,
		"-var_stream_map", strings.Join(variantStreams(l), " "),
		"-master_pl_name", out.Entry,
		"-hls_segment_filename", filepath.Join(dir, "seg_%v_%03d"+ext),
		filepath.Join(dir, "rendition_%v.m3u8"))
	return out
}

// variantStreams is ffmpeg's var_stream_map, one entry per rendition: audio inside each, or in a group of its own.
func variantStreams(l origin.Layout) []string {
	var streams []string
	for i := range l.Rungs {
		entry := fmt.Sprintf("v:%d", i)
		if l.Audio && l.Carriage == origin.Muxed {
			entry += fmt.Sprintf(",a:%d", i)
		}
		streams = append(streams, entry)
	}
	if l.Audio && l.Carriage == origin.Separate {
		for i := range streams {
			streams[i] += ",agroup:aud"
		}
		streams = append(streams, "a:0,agroup:aud")
	}
	return streams
}
