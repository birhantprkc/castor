package dash

import (
	"slices"
	"strings"

	"github.com/stupside/castor/e2e/origin"
)

// Time addresses segments by $Time$ rather than $Number$, the SegmentTimeline form live encoders favour.
type Time struct{}

func (Time) Name() string { return "dash-time" }

func (Time) Supports(l origin.Layout) error { return Packager{}.Supports(l) }

func (Time) Package(dir string, l origin.Layout) origin.Output {
	out := Packager{}.Package(dir, l)
	i := slices.Index(out.Args, "-media_seg_name") + 1
	out.Args[i] = strings.Replace(out.Args[i], "$Number%05d$", "$Time$", 1)
	// Otherwise AAC priming names the first audio segment chunk-1--1024 while the timeline says t=0.
	out.Args = slices.Insert(out.Args, len(out.Args)-1, "-avoid_negative_ts", "make_zero")
	return out
}
