package dash

import (
	"context"
	"encoding/xml"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// DASH parser: reads presentation only; doesn't expand SegmentTemplate.

// mpdDocument is the DASH presentation as castor reads it.
type mpdDocument struct {
	// Video representations in document order (matches ffmpeg stream order).
	Renditions []source.Rendition

	// Live declaration from manifest, not inference.
	Live bool

	// Manifest runtime; zero if live or absent.
	Duration time.Duration

	// ContentProtection present; property of read, not refusal.
	Encrypted bool
}

// Minimal MPD structure; deliberate omissions avoid accidental dependencies.
type mpdRoot struct {
	Type     string `xml:"type,attr"`
	Duration string `xml:"mediaPresentationDuration,attr"`
	Periods  []struct {
		Sets []struct {
			ContentType     string     `xml:"contentType,attr"`
			MimeType        string     `xml:"mimeType,attr"`
			Protection      []struct{} `xml:"ContentProtection"`
			Representations []struct {
				MimeType  string `xml:"mimeType,attr"`
				Codecs    string `xml:"codecs,attr"`
				Bandwidth int64  `xml:"bandwidth,attr"`
				Height    int    `xml:"height,attr"`
			} `xml:"Representation"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

// mpdFrom decodes presentation; malformed body is not an error.
func mpdFrom(body string) (mpdDocument, bool) {
	var root mpdRoot
	if err := xml.Unmarshal([]byte(body), &root); err != nil {
		return mpdDocument{}, false
	}
	doc := mpdDocument{
		// Only dynamic means live; missing type defaults to static.
		Live:     strings.EqualFold(root.Type, "dynamic"),
		Duration: parseISODuration(root.Duration),
	}
	for _, period := range root.Periods {
		for _, set := range period.Sets {
			if len(set.Protection) > 0 {
				doc.Encrypted = true
			}
			for _, rep := range set.Representations {
				// Video ID by manifest pair, not codec; height implies picture.
				if !carriesPicture(set.ContentType, set.MimeType, rep.MimeType, rep.Height) {
					continue
				}
				// Codecs: RFC 6381; HLS same rule; declaration for all rungs behind one URL.
				doc.Renditions = append(doc.Renditions, source.Rendition{
					Index:    len(doc.Renditions),
					Height:   rep.Height,
					Bitrate:  media.Bitrate(rep.Bandwidth),
					Declared: source.DeclaredEnvelope(rep.Codecs, rep.Height),
				})
			}
		}
	}
	// No video representations = not a valid presentation.
	return doc, len(doc.Renditions) > 0
}

// carriesPicture checks if representation carries video.
func carriesPicture(setType, setMIME, repMIME string, height int) bool {
	if height > 0 {
		return true
	}
	for _, s := range []string{setType, setMIME, repMIME} {
		if strings.HasPrefix(strings.ToLower(s), "video") {
			return true
		}
	}
	return false
}

// Parses ISO 8601 duration; hand-implemented for media subset only.
func parseISODuration(s string) time.Duration {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "PT")
	if !ok || rest == "" {
		return 0
	}
	var total time.Duration
	var number strings.Builder
	units := map[byte]time.Duration{'H': time.Hour, 'M': time.Minute, 'S': time.Second}
	for i := range len(rest) {
		c := rest[i]
		if (c >= '0' && c <= '9') || c == '.' {
			number.WriteByte(c)
			continue
		}
		unit, known := units[c&^0x20]
		if !known || number.Len() == 0 {
			return 0
		}
		value, err := strconv.ParseFloat(number.String(), 64)
		if err != nil {
			return 0
		}
		total += time.Duration(value * float64(unit))
		number.Reset()
	}
	// A trailing number with no designator is a malformed duration, not a count of seconds.
	if number.Len() > 0 {
		return 0
	}
	return total
}

// readPresentation fetches and reads manifest; best-effort; facts unknown on failure.
func readPresentation(ctx context.Context, playlists source.Playlists, stream source.Candidate) (mpdDocument, bool) {
	// mpdFrom reads no URIs out of the manifest, so where it was served from is not a term here.
	body, _, status, err := playlists.Fetch(ctx, stream.URL, stream.Headers)
	if err != nil {
		// 403=expired link; 0=transient; log both for opposite responses.
		slog.WarnContext(ctx, "the DASH manifest could not be read; its ladder, runtime and liveness stay unknown",
			"error", err, "status", status, "url", stream.URL.String())
		return mpdDocument{}, false
	}
	doc, ok := mpdFrom(body)
	if !ok {
		slog.WarnContext(ctx, "the document at this URL is not a DASH presentation castor can read; its facts stay unknown",
			"url", stream.URL.String())
	}
	return doc, ok
}

// Pairs manifest declaration with probe measurement by position.
func mergeDeclared(measured, declared []source.Rendition) []source.Rendition {
	if len(declared) == 0 {
		return measured
	}
	if len(measured) == 0 {
		return declared
	}
	if len(measured) != len(declared) {
		return measured
	}
	out := make([]source.Rendition, len(measured))
	for i := range measured {
		out[i] = measured[i]
		out[i].Bitrate = declared[i].Bitrate
		if out[i].Height == 0 {
			out[i].Height = declared[i].Height
		}
		if envelope := declared[i].Declared; envelope != nil {
			stated := envelope.Clone()
			stated.VideoHeight = out[i].Height
			out[i].Declared = &stated
		}
	}
	return out
}
