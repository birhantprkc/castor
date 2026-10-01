package rank

import (
	"cmp"
	"slices"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
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
	if a.LastResort != b.LastResort {
		if b.LastResort {
			return 1 // a was measured, b was not: a wins
		}
		return -1
	}
	if ao, bo := exceedsCap(a, ceiling), exceedsCap(b, ceiling); ao != bo {
		if bo {
			return 1 // a is within the cap, b exceeds it: a wins
		}
		return -1
	}
	// The only signal here about RECOVERY rather than the picture: a master carries rungs to fall back to.
	if al, bl := carriesLadder(a), carriesLadder(b); al != bl {
		if al {
			return 1 // a publishes a ladder, b does not: a wins
		}
		return -1
	}
	// A height is evidence that the selected track is a picture; zero is unknown, not short.
	if ah, bh := height(a) > 0, height(b) > 0; ah != bh {
		if ah {
			return 1
		}
		return -1
	}
	if taller := cmp.Compare(height(a), height(b)); taller != 0 {
		return taller
	}
	if wider := cmp.Compare(a.Bitrate(), b.Bitrate()); wider != 0 {
		return wider
	}
	// Smaller is deliberately preferred only as a reproducible final tie-break.
	return cmp.Compare(b.URL.String(), a.URL.String())
}

// ranked is the ordering a cast walks: best first, every admitted candidate present.
func ranked(pool []*source.Stream, ceiling media.HeightCap) []*source.Stream {
	order := slices.Clone(pool)
	slices.SortStableFunc(order, func(a, b *source.Stream) int { return preference(b, a, ceiling) })
	return order
}
