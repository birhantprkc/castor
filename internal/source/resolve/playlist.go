package resolve

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/grafov/m3u8"

	"github.com/stupside/castor/internal/media"
)

// This file reads HLS documents and nothing else: it decides nothing about which
// rendition to take (see program.go) and asks the network for nothing (see
// ports.go). What it produces is the facts the document states, which is why every
// one of them is testable against a fixture file and none of them costs a probe.

// hlsVariant is a single variant stream listed in an HLS master playlist.
type hlsVariant struct {
	URL       *url.URL
	Bandwidth int64
	Height    int // display height from RESOLUTION; 0 when the master omits it

	// AudioGroup is the GROUP-ID this variant plays its audio from, empty when
	// the audio is muxed into the variant's own segments. A master that names a
	// group publishes the audio as a separate rendition (see hlsDocument.Audio),
	// so the variant alone is video and silence.
	AudioGroup string

	// HasVideo reports whether the variant carries video. A master may list an
	// audio-only rendition as a variant of its own, which must never be picked
	// as the thing to cast.
	HasVideo bool
}

// hlsDocument is a parsed HLS document reduced to what a cast needs from it: the
// variants to choose between, the audio renditions they reference, and the facts
// the document states about the segments themselves. A media playlist reduces to a
// single synthetic variant, so selection treats both shapes uniformly; Multivariant
// is what keeps that normalisation from also hiding which shape it was.
type hlsDocument struct {
	Variants []hlsVariant

	// Audio maps an audio GROUP-ID to the rendition to play from it. Only groups
	// whose rendition has its own URI appear: a rendition without one is carried
	// inside the variant's segments and needs no separate read.
	Audio map[string]*url.URL

	// Live reports that the document is a media playlist with no
	// #EXT-X-ENDLIST, i.e. a live edge. It is always false for a master
	// playlist: the master carries no endlist signal of its own, so callers
	// must not read false as "VOD" in that case.
	Live bool

	// Multivariant reports that the document listed variants of its own, so the
	// single entry in Variants below it is the source's choice and not castor's
	// normalisation of a media playlist. Without it a source offering one 4K rendition
	// and a master castor had capped are the same value (see media.Origin.Sole).
	Multivariant bool

	// Framing is how this document's segments carry their decoder configuration:
	// EXT-X-MAP names a Media Initialization Section, which is what fMP4 segments need
	// and what MPEG-TS segments never have. FramingUnknown is the answer for a master,
	// which lists no segments at all and therefore states nothing about them.
	Framing media.Framing

	// Encrypted reports an EXT-X-KEY declaring a real method somewhere in the
	// document.
	Encrypted bool

	// Duration is the EXTINF sum, and only for a document that ended: it is the real
	// runtime of a VOD program, and the number ffprobe routinely cannot report for a
	// playlist it read perfectly well. It stays 0 on a sliding window, where the sum is
	// the length of the window and not of the program, and reporting that as a runtime
	// would call a two hour title five minutes long.
	Duration time.Duration
}

// AudioFor returns the rendition URL a variant reads its audio from, or nil when
// the variant's own segments carry it.
func (d hlsDocument) AudioFor(v hlsVariant) *url.URL { return d.Audio[v.AudioGroup] }

// parsePlaylist decodes an HLS document into the variants it offers, the audio
// renditions they reference and what it states about its own segments, resolving
// every URI against baseURL.
//
// The document is decoded by m3u8, not by us: the tag grammar is a spec surface
// (quoted attribute values carrying the same comma that separates attributes,
// I-frame variants that state their URI inline, renditions declared before or
// after the variants that use them), and hand-reading it line by line gets those
// subtly wrong in ways that surface as a cast with no sound. Decoding is
// non-strict so a real-world playlist with an unknown tag still parses. What
// stays here is only the part that is castor's policy rather than the format's:
// which variants are castable, which rendition their audio comes from, and which
// of the tags the decoder already parsed change how the source must be read.
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

// mediaFrom reads a media playlist: the document that lists the actual segments,
// and therefore the only one that can answer how they are framed, whether they are
// encrypted, how long the program runs and whether it ends at all.
//
// It is also the shape a source's own URL usually turns out to be, so it normalises
// to a single synthetic variant (the document is itself the thing to read) and
// leaves Multivariant false to record that the source offered nothing to choose
// between.
func mediaFrom(p *m3u8.MediaPlaylist, baseURL *url.URL) hlsDocument {
	doc := hlsDocument{
		Variants: []hlsVariant{{URL: baseURL, HasVideo: true}},
		// This is the one shape that reports its own liveness: no #EXT-X-ENDLIST means
		// the publisher intends to append more segments, i.e. a live edge.
		Live: !p.Closed,
		// Absent EXT-X-MAP the segments are self-contained, which in HLS means MPEG-TS
		// carrying its parameter sets in band ahead of every frame. EXT-X-MAP is the
		// fMP4 tell: the segments are bare fragments whose configuration lives once, in
		// an initialisation section fetched separately. That distinction decides whether
		// abandoning a segment mid-read is survivable, so it is worth the read.
		Framing:   media.FramingInBand,
		Encrypted: encrypted(p.Key),
	}
	if p.Map != nil {
		doc.Framing = media.FramingOutOfBand
	}

	var extinf float64
	// GetAllSegments is the library's own accessor rather than a walk over
	// p.Segments: the slice is a ring buffer with capacity beyond what was decoded,
	// so indexing it directly reads nils past the end of a short playlist.
	for _, segment := range p.GetAllSegments() {
		if segment == nil {
			continue
		}
		extinf += segment.Duration
		// A key can change mid-playlist, and the shape this walk exists for is a document
		// that declares itself clear and then turns encryption ON. The decoder hoists the
		// FIRST key it saw onto the playlist, so an explicit METHOD=NONE ahead of the
		// opening segments becomes the playlist-level key, and reading that alone calls
		// the whole source unencrypted while its later segments need one. A document that
		// merely starts clear and acquires a key (no leading tag at all) is already
		// answered by the playlist key, since that first key is the one hoisted.
		doc.Encrypted = doc.Encrypted || encrypted(segment.Key)
	}
	// Only a document that ended states a runtime: on a sliding window the same sum is
	// the length of the window (see hlsDocument.Duration).
	if p.Closed {
		doc.Duration = time.Duration(extinf * float64(time.Second))
	}
	return doc
}

// encrypted reads an EXT-X-KEY as the presence of encryption. The tag alone is not
// the fact: METHOD=NONE is how a playlist declares that the segments after it are
// in the clear again, so a document that switches encryption off would otherwise
// report itself encrypted for the sake of the tag that says it is not. A tag with
// no method at all states nothing, and stating nothing is the lenient answer here.
func encrypted(key *m3u8.Key) bool {
	return key != nil && key.Method != "" && !strings.EqualFold(key.Method, "NONE")
}

// masterFrom reduces a decoded master to the variants castor can cast and the
// audio renditions they reference. A master lists no segments, so it states nothing
// about framing, encryption or runtime: those come from the chosen variant's own
// document (see Resolver.program).
func masterFrom(playlist *m3u8.MasterPlaylist, baseURL *url.URL) hlsDocument {
	out := hlsDocument{Multivariant: true}
	for _, variant := range playlist.Variants {
		if variant == nil {
			continue
		}
		// An I-frame playlist is a trick-play track: keyframes only, no audio,
		// never something to cast.
		if variant.Iframe {
			continue
		}
		variantURL, err := baseURL.Parse(variant.URI)
		if err != nil {
			continue
		}

		out.Variants = append(out.Variants, hlsVariant{
			URL:        variantURL,
			Bandwidth:  int64(variant.Bandwidth),
			Height:     resolutionHeight(variant.Resolution),
			AudioGroup: variant.Audio,
			HasVideo:   carriesVideo(variant),
		})

		// Renditions are attached to the variant that follows them, so a master
		// that declares its groups once up front hangs them all off its first
		// variant. Collecting from every variant is what makes the group map
		// whole regardless of where they were declared.
		for _, alternative := range variant.Alternatives {
			out.addRendition(alternative, baseURL)
		}
	}
	return out
}

// addRendition records an audio rendition that has a URI of its own. One without
// a URI is muxed into the variants that reference it, so there is nothing extra
// to read. Within a group, DEFAULT=YES wins over the first seen, which is the
// rendition a player picks with no preference of its own.
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

// carriesVideo reports whether a variant has video to cast. Positive evidence is
// a RESOLUTION or a video codec; only a CODECS list that names none rules video
// out. A variant that declares neither attribute is trusted rather than
// discarded, the same way an unknown height stays eligible for the height cap.
func carriesVideo(variant *m3u8.Variant) bool {
	return resolutionHeight(variant.Resolution) > 0 ||
		variant.Codecs == "" ||
		hasVideoCodec(variant.Codecs)
}

// resolutionHeight reads the height out of a RESOLUTION attribute ("1920x1080"),
// returning 0 when the master omits or malforms it.
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

// videoCodecPrefixes are the RFC 6381 sample-entry prefixes for video, used to
// tell a video variant from an audio-only one by its CODECS attribute.
var videoCodecPrefixes = []string{"avc1", "avc3", "hvc1", "hev1", "av01", "vp08", "vp09", "dvh1", "dvhe", "mp4v"}

// hasVideoCodec reports whether a CODECS attribute names a video codec.
func hasVideoCodec(codecs string) bool {
	for codec := range strings.SplitSeq(codecs, ",") {
		codec = strings.TrimSpace(codec)
		if slices.ContainsFunc(videoCodecPrefixes, func(prefix string) bool {
			return strings.HasPrefix(codec, prefix)
		}) {
			return true
		}
	}
	return false
}
