// Package hls is castor's HLS reader: resolves a playlist to the rendition a cast reads.
package hls

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

// Format is HLS as a source.Format.
type Format struct{}

var _ source.Format = Format{}

func (Format) Identity() source.Identity {
	return source.Identity{
		ContentType: media.HLS,
		Extensions:  []string{".m3u8"},
		MIMETypes:   []string{"application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl"},
	}
}

// InputArgs names the demuxer, since it refuses odd names, and reads unseekable, or it walks byte ranges without delivering them.
func (Format) InputArgs(segmentRetries int) []string {
	return append([]string{"-f", ffmpeg.FormatHLS, "-http_seekable", "0"}, ffmpeg.HLSTolerances(segmentRetries)...)
}

const (
	// signature is the required first line of every playlist.
	signature = timeline.TagHeader
	// renditionDeclaration declares a rendition with its own attributes (multivariant only).
	renditionDeclaration = "#EXT-X-STREAM-INF"
)

// Recognize reads a line-oriented playlist by its signature, and as a master when it declares renditions.
func (Format) Recognize(body string) source.Reading {
	switch {
	case !strings.Contains(body, signature):
		return source.Reading{}
	case multivariant(body):
		return source.Reading{Ladder: source.LadderMultivariant, Refs: references(body)}
	}
	read := source.Reading{Ladder: source.LadderSole, Refs: references(body)}
	if doc, err := parsePlaylist(body, &url.URL{}, &url.URL{}); err == nil {
		read.Runtime = doc.duration
	}
	return read
}

var reference = regexp.MustCompile(`URI="([^"]*)"`)

// references reads the two things that name a resource in HLS: a non-comment line and a URI attribute.
func references(body string) []string {
	var out []string
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			out = append(out, line)
			continue
		}
		for _, match := range reference.FindAllStringSubmatch(line, -1) {
			out = append(out, match[1])
		}
	}
	return out
}
