package dash

import (
	"slices"

	"github.com/stupside/castor/e2e/origin"
)

// Duration addresses segments by $Number$ over a fixed duration, with no SegmentTimeline to list them.
type Duration struct{}

func (Duration) Name() string { return "dash-duration" }

func (Duration) Supports(l origin.Layout) error { return Packager{}.Supports(l) }

func (Duration) Package(dir string, l origin.Layout) origin.Output {
	out := Packager{}.Package(dir, l)
	out.Args = slices.Insert(out.Args, len(out.Args)-1, "-use_timeline", "0")
	return out
}

func (Duration) Muxes() string { return "mp4" }
