package hls

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// resolveHLS narrows an HLS source to the single rendition to read and returns what the source published.
func resolveHLS(ctx context.Context, env source.Env, stream source.Candidate, origin source.Origin) (source.Origin, source.Rendition, bool) {
	doc, status, err := readPlaylist(ctx, env.Playlists, stream.URL, stream.Headers)
	if err != nil {
		slog.WarnContext(ctx, "HLS playlist resolution failed, using original", "error", err, "status", status)
		// TRUE: unknown capabilities do not license self-fetch or handing to a fetch-only renderer.
		return origin, source.Rendition{}, true
	}
	if len(doc.Variants) == 0 {
		// A master listing nothing but I-frame playlists reduces to no castable rendition.
		slog.WarnContext(ctx, "playlist offers no castable rendition, using original", "url", stream.URL.String())
		return origin, source.Rendition{}, true
	}
	origin.Renditions = ladder(doc)
	chosen := origin.Choose(env.MaxHeight, byBitrate)
	source.ReportRendition(ctx, chosen, origin, env.MaxHeight)

	// Segment facts are stated by the document that LISTS the segments (chosen variant's own playlist).
	segments := doc
	if doc.Multivariant {
		segments, status, err = readPlaylist(ctx, env.Playlists, chosen.URL, stream.Headers)
		if err != nil {
			slog.WarnContext(ctx, "the chosen rendition's playlist could not be read; the source's own facts stay unknown",
				"error", err, "status", status, "url", chosen.URL.String())
			// TRUE: unknown extension compliance does not license self-fetch (false would).
			return origin, chosen, true
		}
		// A rung naming another master states nothing about segments.
		if segments.Multivariant {
			slog.WarnContext(ctx, "the chosen rendition names another multivariant playlist; the source's own facts stay unknown",
				"url", chosen.URL.String())
			return origin, chosen, true
		}
	}
	origin.Framing = segments.Framing
	origin.Encrypted = segments.Encrypted
	// The document that LISTS the segments decides liveness (EXT-X-ENDLIST is proof).
	origin.Live = segments.Live
	if segments.Duration > 0 {
		// The EXTINF sum wins: ffprobe reports no duration for most playlists.
		origin.Duration = segments.Duration
	}
	return origin, chosen, segments.RequiresRelaxedInput
}

// readPlaylist fetches one HLS document and reduces it to the facts it states.
func readPlaylist(ctx context.Context, playlists source.Playlists, u *url.URL, headers http.Header) (hlsDocument, int, error) {
	body, from, status, err := playlists.Fetch(ctx, u, headers)
	if err != nil {
		return hlsDocument{}, status, err
	}
	// Parsed against where the document CAME FROM (see source.Playlists).
	doc, err := parsePlaylist(body, from)
	return doc, status, err
}

// byBitrate prefers the rung the source declared the richest.
func byBitrate(a, b source.Rendition) int { return cmp.Compare(a.Bitrate, b.Bitrate) }

// castable narrows a variant list to those that carry video (audio-only variants must not be picked).
func castable(variants []hlsVariant) []hlsVariant {
	withVideo := slices.DeleteFunc(slices.Clone(variants), func(v hlsVariant) bool {
		return !v.HasVideo
	})
	if len(withVideo) == 0 {
		return variants
	}
	return withVideo
}

// ladder is the choice the source offered: publication order preserved, audio-only rungs excluded.
func ladder(doc hlsDocument) []source.Rendition {
	rungs := castable(doc.Variants)
	out := make([]source.Rendition, len(rungs))
	for i, v := range rungs {
		out[i] = rendition(doc, v)
	}
	return out
}

// rendition translates one HLS variant without dropping its companion audio.
func rendition(doc hlsDocument, v hlsVariant) source.Rendition {
	return source.Rendition{
		URL:      v.URL,
		AudioURL: doc.AudioFor(v),
		Bitrate:  media.Bitrate(v.Bandwidth),
		Height:   v.Height,
		Declared: v.Declared,
	}
}
