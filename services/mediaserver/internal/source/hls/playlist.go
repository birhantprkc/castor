package hls

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

// variant is a single variant stream listed in an HLS master playlist.
type variant struct {
	url       *url.URL
	bandwidth int64
	height    int // display height from RESOLUTION; 0 when the master omits it

	// program is the variant's position among the master's STREAM-INF lines, the id a probe numbers it by.
	program int

	// audioGroup is the GROUP-ID this variant plays its audio from (empty when muxed into segments).
	audioGroup string

	// hasVideo reports whether the variant carries video (audio-only variants must not be picked).
	hasVideo bool

	// declared is the codec envelope the master stated for this variant (a declaration, not measurement).
	declared *media.ProbeInfo
}

// document is a parsed HLS document reduced to what a cast needs.
type document struct {
	variants []variant

	// audio maps an audio GROUP-ID to the rendition to play from it; nil when the default is muxed into the variant.
	audio map[string]*url.URL

	// live reports a media playlist with no #EXT-X-ENDLIST (always false for a master).
	live bool

	// multivariant reports that the document listed variants of its own.
	multivariant bool

	// framing is how this document's segments carry their decoder configuration (EXT-X-MAP = out-of-band).
	framing media.Framing

	// protection is the KEYFORMAT of a key only a DRM licence server can apply, empty when none is in force.
	protection string

	// duration is the EXTINF sum for a document that ended (VOD only; 0 for sliding window).
	duration time.Duration

	// requiresRelaxedInput reports a segment or init resource whose extension a default reader rejects.
	requiresRelaxedInput bool

	// spliced reports EXT-X-DISCONTINUITY: pieces encoded apart, whose parameters and timestamps restart at each seam.
	spliced bool
}

func (d document) audioFor(v variant) *url.URL { return d.audio[v.audioGroup] }

// parsePlaylist reads a document fetched from requested that arrived from from, after any redirect.
func parsePlaylist(body string, requested, from *url.URL) (document, error) {
	if !multivariant(body) {
		listed, err := scanMedia(body, from)
		if err != nil {
			return document{}, fmt.Errorf("reading media playlist: %w", err)
		}
		return mediaFrom(listed, requested), nil
	}
	master := m3u8.NewMasterPlaylist()
	if err := master.DecodeFrom(strings.NewReader(body), false); err != nil {
		return document{}, fmt.Errorf("decoding multivariant playlist: %w", err)
	}
	return masterFrom(master, from), nil
}

// multivariant is a document naming renditions rather than listing segments.
func multivariant(body string) bool {
	return strings.Contains(body, renditionDeclaration) || strings.Contains(body, "#EXT-X-I-FRAME-STREAM-INF")
}

// mediaFrom keeps the link as asked: a redirect's edge may be a one-time token, and every read must mint its own.
func mediaFrom(l listing, requested *url.URL) document {
	doc := document{
		variants: []variant{{url: requested, hasVideo: true}},
		// No #EXT-X-ENDLIST means the publisher intends to append more segments.
		live:                 !l.closed,
		framing:              media.FramingInBand,
		requiresRelaxedInput: l.relaxed,
	}
	var listed time.Duration
	for _, segment := range l.segments {
		listed += segment.Duration
		// EXT-X-MAP means fMP4, whose decoder configuration travels out of band.
		if segment.Map != nil {
			doc.framing = media.FramingOutOfBand
		}
		doc.spliced = doc.spliced || segment.Seam
		if f := segment.Key.Format; f != "" && f != timeline.IdentityFormat && doc.protection == "" {
			doc.protection = f
		}
	}
	// Only a document that ended states a runtime.
	if l.closed {
		doc.duration = listed
	}
	return doc
}

// defaultExtensions are the names a strict HLS reader accepts a resource under.
var defaultExtensions = map[string]struct{}{
	".3gp": {}, ".aac": {}, ".avi": {}, ".ac3": {}, ".eac3": {}, ".flac": {},
	".mkv": {}, ".m3u8": {}, ".m4a": {}, ".m4s": {}, ".m4v": {}, ".mpg": {},
	".mov": {}, ".mp2": {}, ".mp3": {}, ".mp4": {}, ".mpeg": {}, ".mpegts": {},
	".ogg": {}, ".ogv": {}, ".oga": {}, ".ts": {}, ".vob": {}, ".vtt": {},
	".wav": {}, ".webvtt": {}, ".cmfv": {}, ".cmfa": {}, ".ec3": {}, ".fmp4": {},
	".html": {},
}

// relaxedName reports a resource named with an extension a strict HLS reader refuses.
func relaxedName(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	_, ok := defaultExtensions[strings.ToLower(path.Ext(u.Path))]
	return !ok
}

func masterFrom(playlist *m3u8.MasterPlaylist, baseURL *url.URL) document {
	out := document{multivariant: true}
	program := 0
	for _, listed := range playlist.Variants {
		// An I-frame playlist is a trick-play track: keyframes only, no audio, never something to cast.
		if listed.Iframe {
			continue
		}
		for _, alternative := range listed.Alternatives {
			out.addRendition(alternative, baseURL)
		}
		program++
		variantURL, err := baseURL.Parse(listed.URI)
		if err != nil {
			continue
		}

		height := resolutionHeight(listed.Resolution)
		out.variants = append(out.variants, variant{
			url:        variantURL,
			bandwidth:  int64(listed.Bandwidth),
			height:     height,
			program:    program - 1,
			audioGroup: listed.Audio,
			hasVideo:   carriesVideo(listed),
			declared:   source.DeclaredEnvelope(listed.Codecs, height),
		})
	}
	return out
}

// addRendition records the audio rendition a group plays, the default winning; one without URI is muxed into variants.
func (d *document) addRendition(alternative *m3u8.Alternative, baseURL *url.URL) {
	if alternative.Type != "AUDIO" {
		return
	}
	if _, seen := d.audio[alternative.GroupId]; seen && !alternative.Default {
		return
	}
	var renditionURL *url.URL
	switch parsed, err := baseURL.Parse(alternative.URI); {
	case alternative.URI == "" && !alternative.Default, err != nil:
		return
	case alternative.URI != "":
		renditionURL = parsed
	}
	if d.audio == nil {
		d.audio = make(map[string]*url.URL)
	}
	d.audio[alternative.GroupId] = renditionURL
}

// carriesVideo reports whether a variant has video to cast (RESOLUTION or video codec).
func carriesVideo(listed *m3u8.Variant) bool {
	return resolutionHeight(listed.Resolution) > 0 ||
		listed.Codecs == "" ||
		source.DeclaresVideo(listed.Codecs)
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
