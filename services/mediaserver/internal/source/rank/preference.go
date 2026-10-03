package rank

import (
	"cmp"
	"slices"

	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// height is what a probe measured of this candidate's picture, or zero when no measurement established one.
func height(c *source.Stream) int {
	if c.Probe == nil {
		return 0
	}
	return c.Probe.VideoHeight
}

// carriesLadder reports that this candidate's captured document advertises renditions.
func carriesLadder(c *source.Stream) bool { return c.Ladder == source.LadderMultivariant }

// unreadLadder reports a playlist whose renditions nobody could establish, so it may well be a master.
func unreadLadder(c *source.Stream) bool {
	return media.IsSegmented(c.ContentType) && c.Ladder == source.LadderUnknown
}

// byLadderEvidence orders a document that advertises renditions first, then one nobody could read.
func byLadderEvidence(a, b *source.Stream) int {
	evidence := func(c *source.Stream) int {
		switch {
		case carriesLadder(c):
			return 2
		case unreadLadder(c):
			return 1
		}
		return 0
	}
	return cmp.Compare(evidence(b), evidence(a))
}

// exceedsCap reports whether a candidate's measured height is a real ceiling above the cast's.
func exceedsCap(c *source.Stream, ceiling media.HeightCap) bool {
	if carriesLadder(c) || unreadLadder(c) {
		return false
	}
	return !ceiling.Admits(height(c))
}

func preference(a, b *source.Stream, ceiling media.HeightCap) int {
	return cmp.Or(
		// A measured link beats one admitted unmeasured.
		cmp.Compare(boolean(!a.LastResort), boolean(!b.LastResort)),
		cmp.Compare(boolean(!exceedsCap(a, ceiling)), boolean(!exceedsCap(b, ceiling))),
		// The only signal here about RECOVERY rather than the picture: a master carries rungs to fall back to.
		cmp.Compare(boolean(carriesLadder(a)), boolean(carriesLadder(b))),
		// A height is evidence that the selected track is a picture; zero is unknown, not short.
		cmp.Compare(boolean(height(a) > 0), boolean(height(b) > 0)),
		cmp.Compare(height(a), height(b)),
		cmp.Compare(a.Bitrate(), b.Bitrate()),
		// Smaller is deliberately preferred only as a reproducible final tie-break.
		cmp.Compare(b.URL.String(), a.URL.String()),
	)
}

func boolean(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ranked is the ordering a cast walks: best first, every admitted candidate present.
func ranked(pool []*source.Stream, ceiling media.HeightCap) []*source.Stream {
	order := slices.Clone(pool)
	slices.SortStableFunc(order, func(a, b *source.Stream) int { return preference(b, a, ceiling) })
	return order
}
