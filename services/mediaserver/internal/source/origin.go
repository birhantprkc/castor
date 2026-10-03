package source

import (
	"cmp"
	"slices"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Origin is what the source itself publishes about the program castor chose to read.
type Origin struct {
	Renditions []Rendition

	// Segmented reports that the program arrives as many small files rather than one long read.
	Segmented bool

	Framing media.Framing

	Live bool

	// Protection is the DRM the source declared, which castor cannot decrypt; empty for clear or AES-128 media.
	Protection string

	// Spliced reports a program stitched from pieces encoded apart, such as an ad pod.
	Spliced bool

	// Duration is the program's runtime as the source published it, 0 when it did not.
	Duration time.Duration
}

// Sole reports that the source gave castor no choice: it published one rendition, or none that could be read.
func (o Origin) Sole() bool { return len(o.Renditions) < 2 }

// Choose is the rung a cast reads: the best the ceiling admits by preferred, else the shortest on offer.
func (o Origin) Choose(ceiling media.HeightCap, preferred func(a, b Rendition) int) Rendition {
	admitted := slices.DeleteFunc(slices.Clone(o.Renditions), func(r Rendition) bool { return !ceiling.Admits(r.Height) })
	if len(admitted) > 0 {
		return slices.MaxFunc(admitted, preferred)
	}
	return slices.MinFunc(o.Renditions, func(a, b Rendition) int { return cmp.Compare(a.Height, b.Height) })
}
