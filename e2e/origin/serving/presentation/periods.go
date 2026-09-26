package presentation

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/Eyevinn/dash-mpd/mpd"
	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Periods builds a behaviour that cuts a DASH presentation into consecutive Periods of so many segments, as an ad stitcher does, as in `periods: [4, 2, 6]`.
type Periods struct{}

func (Periods) Name() string { return "periods" }

func (Periods) Build(settings yaml.Node) (origin.Behaviour, error) {
	var counts []int
	if err := strategy.Decode(settings, &counts); err != nil {
		return nil, fmt.Errorf("periods: %w", err)
	}
	if len(counts) < 2 || slices.Min(counts) < 1 {
		return nil, errors.New("periods: want at least two segment counts, each at least one")
	}
	return periods(counts), nil
}

type periods []int

func (periods) Name() string { return "periods" }

func (p periods) Wrap(next http.Handler, _ origin.Published) http.Handler {
	return rewrites(next, p.cut)
}

// cut repeats the MPD's one Period once per count, each holding the next segments of every timeline; the last takes the rest.
func (p periods) cut(m *mpd.MPD) error {
	if len(m.Periods) == 0 {
		return errors.New("the MPD has no Period")
	}
	whole := m.Periods[0]
	tls, err := timelines(whole)
	if err != nil {
		return err
	}
	firsts := make([]int, len(p)+1)
	for i, n := range p {
		firsts[i+1] = firsts[i] + n
	}
	last := len(p) - 1
	if slices.ContainsFunc(tls, func(tl timeline) bool { return len(tl.segments) <= firsts[last] }) {
		return fmt.Errorf("periods %v: a timeline has too few segments to reach the last Period", []int(p))
	}
	// The video set comes first, and its segments place every Period.
	video := tls[0]
	zero := video.segments[0].t
	cut := make([]*mpd.Period, len(p))
	for i := range p {
		bounds := func(n int) (int, int) {
			if i == last {
				return firsts[i], n
			}
			return firsts[i], firsts[i+1]
		}
		from, to := bounds(len(video.segments))
		start, end := video.segments[from].t, video.segments[to-1].t+video.segments[to-1].d
		period := whole.Clone()
		period.Id = "p" + strconv.Itoa(i)
		period.Start = seconds(start-zero, video.timescale())
		period.Duration = seconds(end-start, video.timescale())
		// A clone's timelines are the whole Period's, in the same order.
		own, _ := timelines(period)
		for _, tl := range own {
			from, to := bounds(len(tl.segments))
			tl.template.StartNumber = new(tl.startNumber() + uint32(from))
			tl.template.PresentationTimeOffset = new(tl.segments[from].t)
			tl.list(tl.segments[from:to])
		}
		cut[i] = period
	}
	m.Periods = cut
	return nil
}
