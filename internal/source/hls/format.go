// Package hls is castor's HLS reader: resolves a playlist to the rendition a cast reads.
package hls

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/timeline"
)

// Format is HLS as a source.Format (publishes additional graph structure: video/audio rendition pairs).
type Format struct{}

var _ source.Format = Format{}

func (Format) Name() string { return "hls" }

func (Format) Identity() source.Identity {
	return source.Identity{
		ContentType: media.HLS,
		Extensions:  []string{".m3u8"},
		MIMETypes:   []string{"application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl"},
	}
}

// Resolve narrows the source to the rendition to read and builds the program around it.
func (Format) Resolve(ctx context.Context, env source.Env, s source.Subject) (source.Resolution, error) {
	stream, origin := s.Stream, s.Origin
	// The program the link itself names, before a rung is chosen.
	published, err := source.ProgramFor(&stream)
	if err != nil {
		return source.Resolution{Origin: origin}, fmt.Errorf("normalizing HLS program: %w", err)
	}
	origin, chosen, primaryRelaxed := resolveHLS(ctx, env, stream, origin)
	read := stream.URL
	if chosen.URL != nil {
		read = chosen.URL
	}
	// What this document says about the rung it named, backed by what the caller already knew.
	chosen = chosen.BackedBy(s.Chosen)
	audioURL := chosen.AudioURL
	primaryFetch := media.Fetch{
		Segmented: origin.Segmented,
		Framing:   origin.Framing,
		Live:      origin.Live,
		Spliced:   origin.Spliced,
	}

	inputs := []media.Input{{
		ID:                   media.PrimaryInputID,
		URL:                  read,
		Headers:              source.WithSession(stream.Headers, env.Client.Session(read)),
		ContentType:          stream.ContentType,
		RequiresRelaxedInput: primaryRelaxed,
		Fetch:                primaryFetch,
	}}
	tracks := []media.TrackRef{
		{Input: media.PrimaryInputID, Kind: media.TrackVideo, Optional: true},
		{Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true},
	}

	end := media.EndAtLongest
	if audioURL != nil {
		end = media.EndAtShortest
		audioHeaders := source.WithSession(stream.Headers, env.Client.Session(audioURL))
		audioFetch, audioRelaxed := inspectCompanion(ctx, env.Client, audioURL, audioHeaders)
		inputs = append(inputs, media.Input{
			ID:                   media.AudioInputID,
			URL:                  audioURL,
			Headers:              audioHeaders,
			ContentType:          media.HLS,
			RequiresRelaxedInput: audioRelaxed,
			Fetch:                audioFetch,
		})
		// The rendition exists only to carry sound, so its sound is no longer optional.
		tracks[1].Input, tracks[1].Optional = media.AudioInputID, false
		slog.InfoContext(ctx, "source publishes audio separately; both renditions will be read",
			"video", read.String(), "audio", audioURL.String())
	}

	program, err := media.NewProgram(media.Program{
		Inputs: inputs, Tracks: tracks,
		ClockInput: media.PrimaryInputID,
		EndPolicy:  end,
	})
	if err != nil {
		return source.Resolution{Origin: origin, Rendition: chosen}, fmt.Errorf("normalizing HLS program: %w", err)
	}
	program, err = source.Described(program, chosen, published)
	if err != nil {
		return source.Resolution{Origin: origin, Rendition: chosen}, fmt.Errorf("narrowing the HLS program to the chosen rendition: %w", err)
	}
	return source.Resolution{Program: program, Origin: origin, Rendition: chosen}, nil
}

// inspectCompanion reads the audio rendition's media playlist.
func inspectCompanion(ctx context.Context, playlists source.Client, audioURL *url.URL, headers http.Header) (media.Fetch, bool) {
	unknown := media.Fetch{Segmented: true}
	doc, status, err := readPlaylist(ctx, playlists, audioURL, headers)
	if err != nil {
		slog.WarnContext(ctx, "the companion audio playlist could not be read; its fetch characteristics stay unknown",
			"error", err, "status", status, "url", audioURL.String())
		return unknown, false
	}
	if doc.multivariant {
		slog.WarnContext(ctx, "the companion audio URL names a multivariant playlist; its segment characteristics stay unknown",
			"url", audioURL.String())
		return unknown, false
	}
	return media.Fetch{
		Segmented: true,
		Framing:   doc.framing,
		Live:      doc.live,
		Spliced:   doc.spliced,
	}, doc.requiresRelaxedInput
}

// Tokens the grammar is recognised by (matched literally and case-sensitively per format definition).
const (
	// signature is the required first line of every playlist.
	signature = timeline.TagHeader
	// renditionDeclaration declares a rendition with its own attributes (multivariant only).
	renditionDeclaration = "#EXT-X-STREAM-INF"
)

// Recognize reads a line-oriented playlist and identifies it by signature and rendition declaration.
func (Format) Recognize(body string) source.Reading {
	switch {
	case !strings.Contains(body, signature):
		return source.Reading{}
	case strings.Contains(body, renditionDeclaration):
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
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
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
