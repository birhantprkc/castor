// Package presentation holds DASH origins that publish their presentation the hard way: live, in Periods, or indexed.
package presentation

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/dash-mpd/mpd"
	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
	"github.com/stupside/castor/e2e/strategy"
)

// DashLive builds a live DASH edge: the MPD turns dynamic and its timelines grow over time, as in `dash-live: {start: 4, every: 1s}`.
type DashLive struct{}

func (DashLive) Name() string { return "dash-live" }

func (DashLive) Build(settings yaml.Node) (origin.Behaviour, error) {
	d := dashLive{Start: 4, Every: time.Second}
	if err := strategy.Decode(settings, &d); err != nil {
		return nil, fmt.Errorf("dash-live: %w", err)
	}
	// A reader derives the edge from the clock at one segment a beat, so the beat is the packagers' one-second segment.
	if d.Start < 1 || d.Every != time.Second {
		return nil, errors.New("dash-live: want start >= 1 and every: 1s, the packagers' segment duration")
	}
	return d, nil
}

type dashLive struct {
	Start int           `yaml:"start"`
	Every time.Duration `yaml:"every"`
	// Window is how many segments back a Period stays listed; one that ended further back is dropped, 0 keeps all.
	Window int `yaml:"window"`
}

func (dashLive) Name() string { return "dash-live" }
func (dashLive) LiveEdge()    {}

func (d dashLive) Wrap(next http.Handler, p origin.Published) http.Handler {
	var numbered atomic.Bool
	presentations := rewrites(next, func(m *mpd.MPD) error {
		numbered.Store(slices.ContainsFunc(m.Periods, func(p *mpd.Period) bool {
			return slices.ContainsFunc(templates(p), func(t *mpd.SegmentTemplateType) bool { return strings.Contains(t.Media, "$Number") })
		}))
		return d.edge(m, p.Since, time.Now())
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An encoder has not written what its clock says is still to come; one segment a beat, numbered from 1.
		if n, ok := serving.SegmentIndex(r.URL.Path); ok && p.IsSegment(r) && numbered.Load() && n > d.revealed(p.Since, time.Now()) {
			http.Error(w, "not published yet", http.StatusNotFound)
			return
		}
		presentations.ServeHTTP(w, r)
	})
}

func (d dashLive) revealed(since, now time.Time) int { return d.Start + int(now.Sub(since)/d.Every) }

// edge turns m into the MPD a live encoder publishes at now, leaving the static original once every segment is out.
func (d dashLive) edge(m *mpd.MPD, since, now time.Time) error {
	revealed := d.revealed(since, now)
	// Segments are revealed in document order across Periods, each Period's first timeline counting them.
	type published struct {
		period *mpd.Period
		tls    []timeline
		before int
	}
	var spans []published
	total := 0
	for _, p := range m.Periods {
		tls, err := timelines(p)
		if err != nil {
			// A presentation with no timeline lists nothing, so a reader finds its edge from the clock alone.
			count, counted := segmentCount(m)
			if !counted {
				return err
			}
			spans, total = nil, count
			break
		}
		spans = append(spans, published{period: p, tls: tls, before: total})
		total += len(tls[0].segments)
	}
	if revealed >= total {
		return nil
	}
	m.Type = new(mpd.DYNAMIC_TYPE)
	m.MediaPresentationDuration = nil
	// The first segments are already out at since, so the edge sits at now.
	m.AvailabilityStartTime = mpd.ConvertToDateTimeMS(since.Add(-time.Duration(d.Start) * d.Every).UnixMilli())
	m.PublishTime = mpd.ConvertToDateTimeMS(now.UnixMilli())
	m.MinimumUpdatePeriod = mpd.Seconds2DurPtr(1)
	m.TimeShiftBufferDepth = mpd.Seconds2DurPtr(3600)
	m.SuggestedPresentationDelay = mpd.Seconds2DurPtr(3)
	if spans == nil {
		return nil
	}
	m.Periods = nil
	for _, sp := range spans {
		count := len(sp.tls[0].segments)
		shown := min(max(revealed-sp.before, 0), count)
		if shown == 0 || d.Window > 0 && sp.before+count <= revealed-d.Window {
			continue
		}
		for _, tl := range sp.tls {
			tl.list(tl.segments[:min(shown, len(tl.segments))])
		}
		m.Periods = append(m.Periods, sp.period)
	}
	return nil
}

// segmentCount is how many segments a presentation addressed by a fixed duration holds, from its runtime.
func segmentCount(m *mpd.MPD) (int, bool) {
	var first *mpd.SegmentTemplateType
	for _, p := range m.Periods {
		if ts := templates(p); len(ts) > 0 {
			first = ts[0]
			break
		}
	}
	if first == nil || first.Duration == nil || *first.Duration == 0 || m.MediaPresentationDuration == nil {
		return 0, false
	}
	return int(math.Ceil(m.MediaPresentationDuration.Seconds() * float64(first.GetTimescale()) / float64(*first.Duration))), true
}
