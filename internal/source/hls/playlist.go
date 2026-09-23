package hls

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/grafov/m3u8"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// hlsVariant is a single variant stream listed in an HLS master playlist.
type hlsVariant struct {
	URL       *url.URL
	Bandwidth int64
	Height    int // display height from RESOLUTION; 0 when the master omits it

	// AudioGroup is the GROUP-ID this variant plays its audio from (empty when muxed into segments).
	AudioGroup string

	// HasVideo reports whether the variant carries video (audio-only variants must not be picked).
	HasVideo bool

	// Declared is the codec envelope the master stated for this variant (a declaration, not measurement).
	Declared *media.ProbeInfo
}

// hlsDocument is a parsed HLS document reduced to what a cast needs.
type hlsDocument struct {
	Variants []hlsVariant

	// Audio maps an audio GROUP-ID to the rendition to play from it (only groups with their own URI).
	Audio map[string]*url.URL

	// Live reports a media playlist with no #EXT-X-ENDLIST (always false for a master).
	Live bool

	// Multivariant reports that the document listed variants of its own.
	Multivariant bool

	// Framing is how this document's segments carry their decoder configuration (EXT-X-MAP = out-of-band).
	Framing media.Framing

	// Encrypted reports an EXT-X-KEY declaring a real method somewhere in the document.
	Encrypted bool

	// Duration is the EXTINF sum for a document that ended (VOD only; 0 for sliding window).
	Duration time.Duration

	// RequiresRelaxedInput reports a segment or init resource whose extension a default reader rejects.
	RequiresRelaxedInput bool
}

func (d hlsDocument) AudioFor(v hlsVariant) *url.URL { return d.Audio[v.AudioGroup] }

// parsePlaylist decodes an HLS document into the variants it offers and audio renditions they reference.
func parsePlaylist(body string, baseURL *url.URL) (hlsDocument, error) {
	playlist, _, err := m3u8.DecodeFrom(strings.NewReader(body), false)
	if err != nil {
		return hlsDocument{}, fmt.Errorf("decoding playlist: %w", err)
	}

	switch p := playlist.(type) {
	case *m3u8.MediaPlaylist:
		return mediaFrom(p, baseURL), nil
	case *m3u8.MasterPlaylist:
		return masterFrom(p, baseURL), nil
	default:
		return hlsDocument{}, fmt.Errorf("unsupported playlist type %T", playlist)
	}
}

// mediaFrom reads a media playlist (the document that lists actual segments).
func mediaFrom(p *m3u8.MediaPlaylist, baseURL *url.URL) hlsDocument {
	doc := hlsDocument{
		Variants: []hlsVariant{{URL: baseURL, HasVideo: true}},
		// No #EXT-X-ENDLIST means the publisher intends to append more segments.
		Live: !p.Closed,
		// Absent EXT-X-MAP the segments are MPEG-TS; EXT-X-MAP means fMP4 (out-of-band config).
		Framing:   media.FramingInBand,
		Encrypted: encrypted(p.Key),
	}
	if p.Map != nil {
		doc.Framing = media.FramingOutOfBand
		doc.RequiresRelaxedInput = requiresRelaxedHLSInput(p.Map.URI)
	}

	var extinf float64
	// GetAllSegments: the slice is a ring buffer with capacity beyond what was decoded.
	for _, segment := range p.GetAllSegments() {
		if segment == nil {
			continue
		}
		extinf += segment.Duration
		doc.RequiresRelaxedInput = doc.RequiresRelaxedInput || requiresRelaxedHLSInput(segment.URI)
		// A key can change mid-playlist (METHOD=NONE turns encryption off again).
		doc.Encrypted = doc.Encrypted || encrypted(segment.Key)
	}
	// Only a document that ended states a runtime.
	if p.Closed {
		doc.Duration = time.Duration(extinf * float64(time.Second))
	}
	return doc
}

var defaultHLSExtensions = map[string]struct{}{
	".3gp": {}, ".aac": {}, ".avi": {}, ".ac3": {}, ".eac3": {}, ".flac": {},
	".mkv": {}, ".m3u8": {}, ".m4a": {}, ".m4s": {}, ".m4v": {}, ".mpg": {},
	".mov": {}, ".mp2": {}, ".mp3": {}, ".mp4": {}, ".mpeg": {}, ".mpegts": {},
	".ogg": {}, ".ogv": {}, ".oga": {}, ".ts": {}, ".vob": {}, ".vtt": {},
	".wav": {}, ".webvtt": {}, ".cmfv": {}, ".cmfa": {}, ".ec3": {}, ".fmp4": {},
	".html": {},
}

func requiresRelaxedHLSInput(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	_, ok := defaultHLSExtensions[strings.ToLower(path.Ext(u.Path))]
	return !ok
}

// encrypted reads an EXT-X-KEY: METHOD=NONE means clear; no method states nothing (lenient).
func encrypted(key *m3u8.Key) bool {
	return key != nil && key.Method != "" && !strings.EqualFold(key.Method, "NONE")
}

func masterFrom(playlist *m3u8.MasterPlaylist, baseURL *url.URL) hlsDocument {
	out := hlsDocument{Multivariant: true}
	for _, variant := range playlist.Variants {
		if variant == nil {
			continue
		}
		for _, alternative := range variant.Alternatives {
			out.addRendition(alternative, baseURL)
		}
		// An I-frame playlist is a trick-play track: keyframes only, no audio, never something to cast.
		if variant.Iframe {
			continue
		}
		variantURL, err := baseURL.Parse(variant.URI)
		if err != nil {
			continue
		}

		height := resolutionHeight(variant.Resolution)
		out.Variants = append(out.Variants, hlsVariant{
			URL:        variantURL,
			Bandwidth:  int64(variant.Bandwidth),
			Height:     height,
			AudioGroup: variant.Audio,
			HasVideo:   carriesVideo(variant),
			Declared:   source.DeclaredEnvelope(variant.Codecs, height),
		})
	}
	return out
}

// addRendition records an audio rendition that has a URI of its own (one without URI is muxed into variants).
func (d *hlsDocument) addRendition(alternative *m3u8.Alternative, baseURL *url.URL) {
	if alternative == nil || alternative.Type != "AUDIO" || alternative.GroupId == "" || alternative.URI == "" {
		return
	}
	renditionURL, err := baseURL.Parse(alternative.URI)
	if err != nil {
		return
	}
	if _, seen := d.Audio[alternative.GroupId]; seen && !alternative.Default {
		return
	}
	if d.Audio == nil {
		d.Audio = make(map[string]*url.URL)
	}
	d.Audio[alternative.GroupId] = renditionURL
}

// carriesVideo reports whether a variant has video to cast (RESOLUTION or video codec).
func carriesVideo(variant *m3u8.Variant) bool {
	return resolutionHeight(variant.Resolution) > 0 ||
		variant.Codecs == "" ||
		source.DeclaresVideo(variant.Codecs)
}

func resolutionHeight(resolution string) int {
	_, height, ok := strings.Cut(resolution, "x")
	if !ok {
		return 0
	}
	h, err := strconv.Atoi(height)
	if err != nil {
		return 0
	}
	return h
}
