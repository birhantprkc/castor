package presentation

import (
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"

	"github.com/Eyevinn/dash-mpd/mpd"

	"github.com/stupside/castor/e2e/origin/serving"
)

// rewrites edits every DASH presentation next serves as a typed document.
func rewrites(next http.Handler, edit func(m *mpd.MPD) error) http.Handler {
	return serving.RewritesMPD(next, func(doc string) (string, error) {
		m, err := mpd.ReadFromString(doc)
		if err != nil {
			return "", err
		}
		if err := edit(m); err != nil {
			return "", err
		}
		return m.WriteToString("  ", true)
	})
}

// representations is every representation of m, with the index of the Period it plays in.
func representations(m *mpd.MPD) iter.Seq2[int, *mpd.RepresentationType] {
	return func(yield func(int, *mpd.RepresentationType) bool) {
		for n, p := range m.Periods {
			for _, s := range p.AdaptationSets {
				for _, r := range s.Representations {
					if !yield(n, r) {
						return
					}
				}
			}
		}
	}
}

// templates is every SegmentTemplate a Period states, in document order.
func templates(p *mpd.Period) []*mpd.SegmentTemplateType {
	out := []*mpd.SegmentTemplateType{p.SegmentTemplate}
	for _, s := range p.AdaptationSets {
		out = append(out, s.SegmentTemplate)
		for _, r := range s.Representations {
			out = append(out, r.SegmentTemplate)
		}
	}
	return slices.DeleteFunc(out, func(t *mpd.SegmentTemplateType) bool { return t == nil })
}

// segment is one timeline entry, in its template's timescale.
type segment struct{ t, d uint64 }

// timeline is a SegmentTemplate and its entries, each made explicit.
type timeline struct {
	template *mpd.SegmentTemplateType
	segments []segment
}

func (tl timeline) timescale() uint64 { return uint64(tl.template.GetTimescale()) }

func (tl timeline) startNumber() uint32 {
	if n := tl.template.StartNumber; n != nil {
		return *n
	}
	return 1
}

// list rewrites the template's timeline as segs, one explicit entry each.
func (tl timeline) list(segs []segment) {
	entries := make([]*mpd.S, len(segs))
	for i, s := range segs {
		entries[i] = &mpd.S{T: new(s.t), D: s.d}
	}
	tl.template.SegmentTimeline = &mpd.SegmentTimelineType{S: entries}
}

// timelines reads every SegmentTimeline of a Period, in document order.
func timelines(p *mpd.Period) ([]timeline, error) {
	var tls []timeline
	for _, t := range templates(p) {
		if t.SegmentTimeline == nil {
			continue
		}
		segs, err := expand(t.SegmentTimeline.S)
		if err != nil {
			return nil, err
		}
		tls = append(tls, timeline{template: t, segments: segs})
	}
	if len(tls) == 0 {
		return nil, errors.New("the MPD has no SegmentTimeline")
	}
	return tls, nil
}

// expand unrolls the t/d/r shorthand into one explicit entry per segment.
func expand(entries []*mpd.S) ([]segment, error) {
	var segs []segment
	var next uint64
	for _, e := range entries {
		t := next
		if e.T != nil {
			t = *e.T
		}
		if e.D == 0 || e.R < 0 {
			return nil, fmt.Errorf("timeline entry at %d: want a positive d and no open-ended r", t)
		}
		for range e.R + 1 {
			segs = append(segs, segment{t: t, d: e.D})
			t += e.D
		}
		next = t
	}
	return segs, nil
}

// seconds is an xs:duration of ticks in timescale.
func seconds(ticks, timescale uint64) *mpd.Duration {
	return mpd.Seconds2DurPtrFloat64(float64(ticks) / float64(timescale))
}
