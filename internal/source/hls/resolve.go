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
func resolveHLS(ctx context.Context, env source.Env, stream source.Stream, origin source.Origin) (source.Origin, source.Rendition, bool) {
	doc, status, err := readPlaylist(ctx, env.Client, stream.URL, stream.Headers)
	if err != nil {
		slog.WarnContext(ctx, "HLS playlist resolution failed, using original", "error", err, "status", status)
		// TRUE: unknown capabilities do not license self-fetch or handing to a fetch-only renderer.
		return origin, source.Rendition{}, true
	}
	if len(doc.variants) == 0 {
		// A master listing nothing but I-frame playlists reduces to no castable rendition.
		slog.WarnContext(ctx, "playlist offers no castable rendition, using original", "url", stream.URL.String())
		return origin, source.Rendition{}, true
	}
	origin.Renditions = ladder(doc, stream.Probe)
	chosen := origin.Choose(env.MaxHeight, byBitrate)
	source.ReportRendition(ctx, chosen, origin, env.MaxHeight)

	// Segment facts are stated by the document that LISTS the segments (chosen variant's own playlist).
	segments := doc
	if doc.multivariant {
		segments, status, err = readPlaylist(ctx, env.Client, chosen.URL, stream.Headers)
		if err != nil {
			slog.WarnContext(ctx, "the chosen rendition's playlist could not be read; the source's own facts stay unknown",
				"error", err, "status", status, "url", chosen.URL.String())
			// TRUE: unknown extension compliance does not license self-fetch (false would).
			return origin, chosen, true
		}
		// A rung naming another master states nothing about segments.
		if segments.multivariant {
			slog.WarnContext(ctx, "the chosen rendition names another multivariant playlist; the source's own facts stay unknown",
				"url", chosen.URL.String())
			return origin, chosen, true
		}
	}
	origin.Framing = segments.framing
	origin.Protection = segments.protection
	origin.Spliced = segments.spliced
	// The document that LISTS the segments decides liveness (EXT-X-ENDLIST is proof).
	origin.Live = segments.live
	if segments.duration > 0 {
		// The EXTINF sum wins: ffprobe reports no duration for most playlists.
		origin.Duration = segments.duration
	}
	return origin, chosen, segments.requiresRelaxedInput
}

// readPlaylist fetches one HLS document and reduces it to the facts it states.
func readPlaylist(ctx context.Context, playlists source.Client, u *url.URL, headers http.Header) (hlsDocument, int, error) {
	body, from, status, err := playlists.Fetch(ctx, u, headers)
	if err != nil {
		return hlsDocument{}, status, err
	}
	// A master's references resolve against where it CAME FROM (see source.Client).
	doc, err := parsePlaylist(body, u, from)
	return doc, status, err
}

// byBitrate prefers the rung the source declared the richest, the taller when two declare the same.
func byBitrate(a, b source.Rendition) int {
	return cmp.Or(cmp.Compare(a.Bitrate, b.Bitrate), cmp.Compare(a.Height, b.Height))
}

// castable narrows a variant list to those that carry video (audio-only variants must not be picked).
func castable(doc hlsDocument) []hlsVariant {
	withVideo := slices.DeleteFunc(slices.Clone(doc.variants), func(v hlsVariant) bool {
		audio := doc.audioFor(v)
		return !v.hasVideo || (audio != nil && v.url.String() == audio.String())
	})
	if len(withVideo) == 0 {
		return doc.variants
	}
	return withVideo
}

// ladder is the choice the source offered: publication order preserved, audio-only rungs excluded.
func ladder(doc hlsDocument, probe *media.ProbeInfo) []source.Rendition {
	measured := measuredHeights(doc, probe)
	rungs := castable(doc)
	out := make([]source.Rendition, len(rungs))
	for i, v := range rungs {
		out[i] = rendition(doc, v, measured[v.program])
	}
	return out
}

// measuredHeights is each variant's picture as a probe of this very master measured it, by program.
func measuredHeights(doc hlsDocument, probe *media.ProbeInfo) map[int]int {
	if probe == nil || !doc.multivariant {
		return nil
	}
	// A probe that saw another program count read another document than the one parsed.
	variants := 0
	for _, v := range doc.variants {
		variants = max(variants, v.program+1)
	}
	if len(probe.ProgramHeights) != variants {
		return nil
	}
	return probe.ProgramHeights
}

// rendition translates one HLS variant without dropping its companion audio; measurement wins over RESOLUTION.
func rendition(doc hlsDocument, v hlsVariant, measured int) source.Rendition {
	height := cmp.Or(measured, v.height)
	declared := v.declared
	if declared != nil && declared.VideoHeight != height {
		stated := declared.Clone()
		stated.VideoHeight = height
		declared = &stated
	}
	return source.Rendition{
		URL:      v.url,
		AudioURL: doc.audioFor(v),
		Bitrate:  media.Bitrate(v.bandwidth),
		Height:   height,
		Declared: declared,
	}
}
