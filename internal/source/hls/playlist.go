package hls

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/timeline"
)

// hlsVariant is a single variant stream listed in an HLS master playlist.
type hlsVariant struct {
	URL       *url.URL
	Bandwidth int64
	Height    int // display height from RESOLUTION; 0 when the master omits it

	// Program is the variant's position among the master's STREAM-INF lines, the id a probe numbers it by.
	Program int

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

	// Audio maps an audio GROUP-ID to the rendition to play from it; nil when the default is muxed into the variant.
	Audio map[string]*url.URL

	// Live reports a media playlist with no #EXT-X-ENDLIST (always false for a master).
	Live bool

	// Multivariant reports that the document listed variants of its own.
	Multivariant bool

	// Framing is how this document's segments carry their decoder configuration (EXT-X-MAP = out-of-band).
	Framing media.Framing

	// Protection is the KEYFORMAT of a key only a DRM licence server can apply, empty when none is in force.
	Protection string

	// Duration is the EXTINF sum for a document that ended (VOD only; 0 for sliding window).
	Duration time.Duration

	// RequiresRelaxedInput reports a segment or init resource whose extension a default reader rejects.
	RequiresRelaxedInput bool

	// Spliced reports EXT-X-DISCONTINUITY: pieces encoded apart, whose parameters and timestamps restart at each seam.
	Spliced bool
}

func (d hlsDocument) AudioFor(v hlsVariant) *url.URL { return d.Audio[v.AudioGroup] }

// parsePlaylist reads a document fetched from requested that arrived from from, after any redirect.
func parsePlaylist(body string, requested, from *url.URL) (hlsDocument, error) {
	if !multivariant(body) {
		listed, err := scanMedia(body, from)
		if err != nil {
			return hlsDocument{}, fmt.Errorf("reading media playlist: %w", err)
		}
		return mediaFrom(listed, requested), nil
	}
	master := m3u8.NewMasterPlaylist()
	if err := master.DecodeFrom(strings.NewReader(body), false); err != nil {
		return hlsDocument{}, fmt.Errorf("decoding multivariant playlist: %w", err)
	}
	return masterFrom(master, from), nil
}

// multivariant is a document naming renditions rather than listing segments.
func multivariant(body string) bool {
	return strings.Contains(body, renditionDeclaration) || strings.Contains(body, "#EXT-X-I-FRAME-STREAM-INF")
}

// mediaFrom keeps the link as asked: a redirect's edge may be a one-time token, and every read must mint its own.
func mediaFrom(l listing, requested *url.URL) hlsDocument {
	doc := hlsDocument{
		Variants: []hlsVariant{{URL: requested, HasVideo: true}},
		// No #EXT-X-ENDLIST means the publisher intends to append more segments.
		Live:                 !l.Closed,
		Framing:              media.FramingInBand,
		RequiresRelaxedInput: l.Relaxed,
	}
	var listed time.Duration
	for _, segment := range l.Segments {
		listed += segment.Duration
		// EXT-X-MAP means fMP4, whose decoder configuration travels out of band.
		if segment.Map != nil {
			doc.Framing = media.FramingOutOfBand
		}
		doc.Spliced = doc.Spliced || segment.Seam
		if f := segment.Key.Format; f != "" && f != timeline.IdentityFormat && doc.Protection == "" {
			doc.Protection = f
		}
	}
	// Only a document that ended states a runtime.
	if l.Closed {
		doc.Duration = listed
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

func masterFrom(playlist *m3u8.MasterPlaylist, baseURL *url.URL) hlsDocument {
	out := hlsDocument{Multivariant: true}
	program := 0
	for _, variant := range playlist.Variants {
		// An I-frame playlist is a trick-play track: keyframes only, no audio, never something to cast.
		if variant.Iframe {
			continue
		}
		for _, alternative := range variant.Alternatives {
			out.addRendition(alternative, baseURL)
		}
		program++
		variantURL, err := baseURL.Parse(variant.URI)
		if err != nil {
			continue
		}

		height := resolutionHeight(variant.Resolution)
		out.Variants = append(out.Variants, hlsVariant{
			URL:        variantURL,
			Bandwidth:  int64(variant.Bandwidth),
			Height:     height,
			Program:    program - 1,
			AudioGroup: variant.Audio,
			HasVideo:   carriesVideo(variant),
			Declared:   source.DeclaredEnvelope(variant.Codecs, height),
		})
	}
	return out
}

// addRendition records the audio rendition a group plays, the default winning; one without URI is muxed into variants.
func (d *hlsDocument) addRendition(alternative *m3u8.Alternative, baseURL *url.URL) {
	if alternative.Type != "AUDIO" {
		return
	}
	if _, seen := d.Audio[alternative.GroupId]; seen && !alternative.Default {
		return
	}
	var renditionURL *url.URL
	switch parsed, err := baseURL.Parse(alternative.URI); {
	case alternative.URI == "" && !alternative.Default, err != nil:
		return
	case alternative.URI != "":
		renditionURL = parsed
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
