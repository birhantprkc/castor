package dash

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Eyevinn/dash-mpd/mpd"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// presentation is a DASH presentation as dash-mpd reads it.
type presentation struct{ *mpd.MPD }

// parse reads a presentation; a document that is not one reads as nothing.
func parse(body string) (presentation, bool) {
	m, err := mpd.ReadFromString(calendar.ReplaceAllStringFunc(body, fixedLength))
	if err != nil || len(m.Periods) == 0 {
		return presentation{}, false
	}
	return presentation{m}, true
}

// calendar is an xs:duration in years or months, which dash-mpd refuses though packagers write them as zeros.
var calendar = regexp.MustCompile(`="P((?:\d+[YM])+)(\d+D)?(T[\d.HMS]+)?"`)

// fixedLength restates a calendar duration in days and time; a stated year or month has no fixed length, so it reads as nothing.
func fixedLength(attr string) string {
	parts := calendar.FindStringSubmatch(attr)
	if strings.Trim(parts[1], "0YM") != "" || parts[2]+parts[3] == "" {
		return `="PT0S"`
	}
	return `="P` + parts[2] + parts[3] + `"`
}

func (m presentation) live() bool { return strings.EqualFold(m.GetType(), mpd.DYNAMIC_TYPE) }

// span is a duration the document states, zero when it states none.
func span(d *mpd.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return time.Duration(*d)
}

// instants are the xs:dateTime spellings packagers write; one with no zone is UTC, as DASH-IF requires.
var instants = []string{mpd.RFC3339MS, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02T15:04:05.999999999"}

// instant reads an xs:dateTime, the zero time when it is absent or unreadable.
func instant(d mpd.DateTime) time.Time {
	for _, layout := range instants {
		if t, err := time.Parse(layout, strings.TrimSpace(string(d))); err == nil {
			return t
		}
	}
	return time.Time{}
}

// placed is a Period with its start and length settled from its own attributes, its neighbours, or the presentation's.
type placed struct {
	*mpd.Period
	index    int
	start    time.Duration
	duration time.Duration
}

// key names a Period the same way across refreshes of a live presentation.
func (p placed) key() string { return cmp.Or(p.Id, "@"+p.start.String()) }

func (m presentation) placed() []placed {
	out := make([]placed, len(m.Periods))
	var next time.Duration
	for i, p := range m.Periods {
		start := next
		if p.Start != nil {
			start = span(p.Start)
		}
		out[i] = placed{Period: p, index: i, start: start, duration: span(p.Duration)}
		if i > 0 && out[i-1].duration == 0 {
			out[i-1].duration = start - out[i-1].start
		}
		next = start + out[i].duration
	}
	if last := &out[len(out)-1]; last.duration == 0 {
		if total := span(m.MediaPresentationDuration); total > last.start {
			last.duration = total - last.start
		}
	}
	return out
}

// resolveBase walks a BaseURL chain from where the presentation was served, each level relative to the one above.
func resolveBase(from *url.URL, levels ...[]*mpd.BaseURLType) *url.URL {
	base := from
	for _, level := range levels {
		if len(level) == 0 {
			continue
		}
		if next, err := base.Parse(strings.TrimSpace(string(level[0].Value))); err == nil {
			base = next
		}
	}
	return base
}

// kindText is subtitles, which castor never casts.
const kindText media.TrackKind = "text"

// kind is what a representation carries, as its set or itself declares it; only an undeclared one is judged by its picture.
func kind(s *mpd.AdaptationSetType, r *mpd.RepresentationType) media.TrackKind {
	for _, declared := range []string{string(s.ContentType), r.MimeType, s.MimeType} {
		if k, _, _ := strings.Cut(strings.ToLower(declared), "/"); k != "" {
			// Subtitles packaged as fMP4 declare application/mp4, and only their codecs say what they carry.
			if k == "application" && slices.ContainsFunc([]string{"stpp", "wvtt"}, func(c string) bool { return strings.Contains(codecs(s, r), c) }) {
				return kindText
			}
			return media.TrackKind(k)
		}
	}
	if r.Height > 0 || s.Height > 0 || s.MaxHeight > 0 {
		return media.TrackVideo
	}
	return ""
}

// height is a representation's picture, its set's when it states none.
func height(s *mpd.AdaptationSetType, r *mpd.RepresentationType) int {
	return int(cmp.Or(r.Height, s.Height, s.MaxHeight))
}

func codecs(s *mpd.AdaptationSetType, r *mpd.RepresentationType) string {
	return cmp.Or(r.Codecs, s.Codecs)
}

// trickMode is a set of keyframes only, for scrubbing, never something to cast (DASH-IF writes it as .../guidelines/trickmode).
func trickMode(s *mpd.AdaptationSetType) bool {
	return slices.ContainsFunc(s.EssentialProperties, func(e *mpd.DescriptorType) bool { return strings.HasSuffix(string(e.SchemeIdUri), "trickmode") })
}

func channels(s *mpd.AdaptationSetType, r *mpd.RepresentationType) int {
	for _, d := range slices.Concat(r.AudioChannelConfigurations, s.AudioChannelConfigurations) {
		if n, err := strconv.Atoi(d.Value); err == nil {
			return n
		}
	}
	return 0
}

func mainRole(s *mpd.AdaptationSetType) bool {
	return slices.ContainsFunc(s.Roles, func(r *mpd.DescriptorType) bool { return r.Value == "main" })
}

// protection is the first content protection a set or representation states, by its value or else its scheme.
func protection(periods []placed) string {
	for _, p := range periods {
		for _, s := range p.AdaptationSets {
			declared := slices.Clone(s.ContentProtections)
			for _, r := range s.Representations {
				declared = append(declared, r.ContentProtections...)
			}
			if len(declared) > 0 {
				return cmp.Or(declared[0].Value, string(declared[0].SchemeIdUri))
			}
		}
	}
	return ""
}
