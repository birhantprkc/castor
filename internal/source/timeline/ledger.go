package timeline

import (
	"slices"
	"time"
)

// Ledger is the only place the republished timeline is decided.
type Ledger struct {
	entries []entry
	// next is the sequence number the next appended segment takes.
	next int64
	// dropped counts the seams trimmed off the front, as EXT-X-DISCONTINUITY-SEQUENCE.
	dropped int64
	// retain is the longest window the origin has published.
	retain  int
	periods map[string]bool
	closed  bool
	start   *Start
	longest time.Duration
}

type entry struct {
	Segment
	sequence int64
}

// Merged is how one window broke the timeline: Restarts where the origin numbered backwards, Gaps where it skipped ahead unannounced.
type Merged struct {
	Restarts, Gaps int
}

// identity is what makes two listings the same segment, whatever number the origin gave it.
type identity struct {
	uri   string
	span  Range
	start int64
}

func (s Segment) identity() identity { return identity{s.URI, s.Range, s.Place.Start} }

// Merge appends what the window lists past the last segment published.
func (l *Ledger) Merge(w Window) Merged {
	if l.closed {
		return Merged{}
	}
	known := make(map[identity]bool, len(l.entries))
	for _, e := range l.entries {
		known[e.identity()] = true
	}
	// A window overlapping what was published continues after its last known segment; a stale one adds nothing.
	fresh := w.Segments
	for i, s := range w.Segments {
		if known[s.identity()] {
			fresh = w.Segments[i+1:]
		}
	}

	var merged Merged
	for _, s := range fresh {
		last, published := l.last()
		switch {
		case !published:
			s.Seam = false
		case s.Place.Period != last.Place.Period:
			if l.periods[s.Place.Period] {
				continue
			}
			s.Seam = true
		case s.Place.Start > last.Place.End:
			// A gap the origin declared is a seam it announced, not a loss.
			if !s.Seam {
				merged.Gaps++
			}
			s.Seam = true
		case s.Place.Start < last.Place.End:
			s.Seam = true
			merged.Restarts++
		}
		l.append(s)
	}

	l.retain = max(l.retain, len(w.Segments))
	l.trim()
	l.start = l.rebase(w)
	l.closed = w.Closed
	return merged
}

// Closed reports an origin that published its end: nothing it lists changes any more.
func (l *Ledger) Closed() bool { return l.closed }

func (l *Ledger) last() (entry, bool) {
	if len(l.entries) == 0 {
		return entry{}, false
	}
	return l.entries[len(l.entries)-1], true
}

func (l *Ledger) append(s Segment) {
	if l.periods == nil {
		l.periods = map[string]bool{}
	}
	l.periods[s.Place.Period] = true
	l.entries = append(l.entries, entry{Segment: s, sequence: l.next})
	l.next++
	l.longest = max(l.longest, s.Duration)
}

// trim keeps as many entries as the origin's longest window, so a reader lagging the edge never loses one castor dropped first.
func (l *Ledger) trim() {
	over := len(l.entries) - l.retain
	if over <= 0 {
		return
	}
	for _, e := range l.entries[:over] {
		if e.Seam {
			l.dropped++
		}
	}
	l.entries = slices.Delete(l.entries, 0, over)
}

// rebase moves a start measured from the origin's first segment onto the ledger's.
func (l *Ledger) rebase(w Window) *Start {
	if w.Start == nil || w.Start.Offset < 0 || len(w.Segments) == 0 {
		return w.Start
	}
	var before time.Duration
	for _, e := range l.entries {
		if e.identity() == w.Segments[0].identity() {
			return &Start{Offset: w.Start.Offset + before, Precise: w.Start.Precise}
		}
		before += e.Duration
	}
	// The origin's first segment was never published here, so its offset measures nothing in this playlist.
	return nil
}

// Entry is the retained segment published under sequence; sequences run unbroken, so it is found by position.
func (l *Ledger) Entry(sequence int64) (Segment, bool) {
	if len(l.entries) == 0 {
		return Segment{}, false
	}
	i := sequence - l.entries[0].sequence
	if i < 0 || i >= int64(len(l.entries)) {
		return Segment{}, false
	}
	return l.entries[i].Segment, true
}
