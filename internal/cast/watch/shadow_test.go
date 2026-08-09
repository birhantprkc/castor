package watch

import (
	"iter"
	"reflect"
	"slices"
	"testing"
	"time"
)

// This file holds the missing mirror of the coupling tests. Those assert that every verdict
// the table REACHES has an action; nothing asserted that every ROW can be reached, and a row
// no walk of the table ever answers with fails nothing at all while reading as coverage.
//
// The property is first-match, per row and per window it claims, over the states the tracker
// can actually hand the table. A row shadowed by one above it and a row whose predicate is
// unsatisfiable are the same defect from out here (nobody is ever answered by it) and they
// are reported apart, because the fixes are opposite: one is an ordering, the other is a
// clause.

// fact is one measurement the table reads, at the values a rule could discriminate on: both
// sides of every derived window, and the zero a port answers with when it is absent. It is
// written as a setter per value rather than as a typed list so one loop can walk facts of
// different types.
type fact struct {
	// name is the Health field, which is what a failure has to name to be actionable.
	name string
	// values each write one value of this fact into a Health.
	values []func(*Health)
}

// facts is the state space every row is searched over. It is the CROSS PRODUCT and not a
// list of cases, because a shadowed row is precisely one no case anybody thought to write
// reaches: the whole point is to enumerate states nobody had in mind.
//
// Each fact carries the two or three values the table can tell apart. A window is probed on
// both sides and never at its boundary, because a rule reading `>` and a rule reading `>=`
// are the same row for this property and pinning the boundary belongs to the tables that
// state the window (see TestNeitherDeliverabilityArmFiresInsideTheBackoffItHandedTheReader).
var facts = []fact{{
	name: "Landed",
	values: []func(*Health){
		func(h *Health) { h.Landed = 0 },
		func(h *Health) { h.Landed = 1 << 20 },
	},
}, {
	name: "SinceGrowth",
	values: []func(*Health){
		func(h *Health) { h.SinceGrowth = 0 },
		func(h *Health) { h.SinceGrowth = StallWindow + time.Second },
	},
}, {
	// No rule reads the position today, and it is varied anyway: the space has to cover every
	// measurement the table CAN read, or a row keyed on a new one would be searched over a
	// space that holds it still (see TestTheStateSpaceCoversEveryFactTheTableCanRead).
	name: "Position",
	values: []func(*Health){
		func(h *Health) { h.Position = 0 },
		func(h *Health) { h.Position = 30 * time.Minute },
	},
}, {
	name: "Ended",
	values: []func(*Health){
		func(h *Health) { h.Ended = false },
		func(h *Health) { h.Ended = true },
	},
}, {
	name: "Failed",
	values: []func(*Health){
		func(h *Health) { h.Failed = false },
		func(h *Health) { h.Failed = true },
	},
}, {
	name: "Overdue",
	values: []func(*Health){
		func(h *Health) { h.Overdue = false },
		func(h *Health) { h.Overdue = true },
	},
}, {
	name: "Subtitles",
	values: []func(*Health){
		func(h *Health) { h.Subtitles = false },
		func(h *Health) { h.Subtitles = true },
	},
}, {
	name: "Lead",
	values: []func(*Health){
		func(h *Health) { h.Lead = 0 },
		func(h *Health) { h.Lead = transcriptionLeadSeconds },
	},
}, {
	name: "LeadDone",
	values: []func(*Health){
		func(h *Health) { h.LeadDone = false },
		func(h *Health) { h.LeadDone = true },
	},
}, {
	name: "Handed",
	values: []func(*Health){
		func(h *Health) { h.Handed = 0 },
		func(h *Health) { h.Handed = 4 << 20 },
	},
}, {
	name: "SinceFetch",
	values: []func(*Health){
		func(h *Health) { h.SinceFetch = 0 },
		func(h *Health) { h.SinceFetch = time.Second },
		func(h *Health) { h.SinceFetch = fetchWindow + time.Second },
	},
}, {
	// Three values, because the pace is the one fact with three meanings rather than two:
	// withheld, a live edge that cannot be outrun, and an allowance to run ahead.
	name: "Headroom",
	values: []func(*Health){
		func(h *Health) { h.Headroom = 0 },
		func(h *Health) { h.Headroom = 1 },
		func(h *Health) { h.Headroom = 2 },
	},
}, {
	name: "Samples",
	values: []func(*Health){
		func(h *Health) { h.Samples = 0 },
		func(h *Health) { h.Samples = minSpeedSamples },
	},
}, {
	name: "Speed",
	values: []func(*Health){
		func(h *Health) { h.Speed = 0.0627 },
		func(h *Health) { h.Speed = 2.1 },
	},
}, {
	name: "SinceDeficit",
	values: []func(*Health){
		func(h *Health) { h.SinceDeficit = 0 },
		func(h *Health) { h.SinceDeficit = time.Second },
		func(h *Health) { h.SinceDeficit = deficitWindow + time.Second },
		func(h *Health) { h.SinceDeficit = StallWindow + time.Second },
	},
}, {
	name: "Delivered",
	values: []func(*Health){
		func(h *Health) { h.Delivered = 0 },
		func(h *Health) { h.Delivered = 20 * time.Minute },
	},
}, {
	name: "SincePlay",
	values: []func(*Health){
		func(h *Health) { h.SincePlay = 0 },
		func(h *Health) { h.SincePlay = 4 * time.Minute },
	},
}}

// states walks the cross product of the facts above, yielding only the ones a tracker can
// produce.
func states() iter.Seq[Health] {
	return func(yield func(Health) bool) {
		at := make([]int, len(facts))
		for {
			var h Health
			for i, f := range facts {
				f.values[at[i]](&h)
			}
			if producible(h) && !yield(h) {
				return
			}
			i := len(at) - 1
			for ; i >= 0; i-- {
				at[i]++
				if at[i] < len(facts[i].values) {
					break
				}
				at[i] = 0
			}
			if i < 0 {
				return
			}
		}
	}
}

// producible drops the states tracker.read cannot hand the table. Searching over them would
// be worse than searching over fewer: a row reachable ONLY through a state no reading
// produces is exactly the dead rule this file exists to catch, so a state space that admits
// them would report it as covered.
//
// Every clause is read off tracker.read and nowhere else.
func producible(h Health) bool {
	switch {
	case h.Failed && !h.Ended:
		// Failed is read inside the branch that saw the producer's Done close, so a failure
		// without a terminal state is not a reading.
		return false
	case (h.Lead != 0 || h.LeadDone) && !h.Subtitles:
		// Both come from the Lead port, and Subtitles IS that port being present.
		return false
	case h.SinceDeficit > 0 && !h.starving():
		// The deficit clock is started by the reading that saw the deficit and cleared by the
		// one that did not, so a duration here means the same reading measured a starving read.
		return false
	case h.Delivered > 0 && h.SincePlay == 0:
		// Both come from the Delivered port, and SincePlay is the watch's own age, which is
		// never zero once it has taken a reading.
		return false
	case h.Handed > 0 && h.SinceFetch == 0:
		// Both come from the Consumer port, and SinceFetch is measured from the moment a byte
		// last moved or from the watch's start, so a renderer that has been handed something has
		// a duration since.
		return false
	}
	return true
}

// TestEveryRuleIsTheFirstMatchForSomeStateItAnswers is the shadowing property. A row that is
// never the first match is unreachable however carefully it is written, and it fails nothing:
// the walk answers with the row above it, the coupling tests still pass because every verdict
// the table REACHES has an action, and the row reads as coverage of a pathology nobody is
// judging.
//
// It is asserted per window as well as per row, because a row claiming a window it can never
// win in is the same defect narrowed: the pathology is documented as answered there and is not.
func TestEveryRuleIsTheFirstMatchForSomeStateItAnswers(t *testing.T) {
	type claim struct {
		rule   string
		window Window
	}
	var (
		won     = map[claim]bool{}
		matched = map[claim]bool{}
		lost    = map[claim]string{}
	)
	for h := range states() {
		for _, w := range []Window{BeforePlay, Opening, Playing} {
			first, _, err := judge(w, h)
			if err != nil {
				t.Fatalf("judge(%s, %s): %v", w, h, err)
			}
			won[claim{first.Name, w}] = true
			for _, r := range rules {
				if r.Name == first.Name || !slices.Contains(r.Windows, w) || !r.When(h) {
					continue
				}
				matched[claim{r.Name, w}] = true
				// The first winner and not the last, so the row named is the one a reader will
				// find answering the shadowed row's own case rather than whichever state the
				// walk happened to end on.
				if _, ok := lost[claim{r.Name, w}]; !ok {
					lost[claim{r.Name, w}] = first.Name
				}
			}
		}
	}

	for _, r := range rules {
		for _, w := range r.Windows {
			c := claim{r.Name, w}
			switch {
			case won[c]:
			case matched[c]:
				t.Errorf("rule %q never answers a cast in the %s window: every state it recognises is taken by %q above it, so the pathology it documents is judged by another row",
					r.Name, w, lost[c])
			default:
				t.Errorf("rule %q recognises no state a reading can produce in the %s window, so it can never fire: its predicate is unsatisfiable there",
					r.Name, w)
			}
		}
	}
}

// TestTheStateSpaceCoversEveryFactTheTableCanRead is what keeps the search above honest as
// Health grows. A measurement the space holds still is a measurement no row can be shown to
// depend on and no row can be shown to be shadowed over, so a rule keyed on a newly added fact
// would be searched over a space where that fact is always its zero value: the search would
// report the row as reachable on the strength of never having varied the thing it reads.
//
// It is asserted by name against the struct rather than by counting, so the failure names the
// fact that has to be given values.
func TestTheStateSpaceCoversEveryFactTheTableCanRead(t *testing.T) {
	varied := map[string]bool{}
	for _, f := range facts {
		if len(f.values) < 2 {
			t.Errorf("fact %q is written with fewer than two values, so nothing about it is ever varied", f.name)
		}
		varied[f.name] = true
	}
	for field := range reflect.TypeFor[Health]().NumField() {
		if name := reflect.TypeFor[Health]().Field(field).Name; !varied[name] {
			t.Errorf("Health.%s is never varied by the state space, so no row can be caught depending on it or shadowed over it", name)
		}
	}
}

// TestEveryRuleHasItsOwnName pins what the property above is keyed on, and what a log line
// and a fault are read by: two rows sharing a name make one of them invisible to every
// assertion here and to anybody reading "rule=..." out of a cast that was abandoned.
func TestEveryRuleHasItsOwnName(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range rules {
		if r.Name == "" {
			t.Error("a rule carries no name, so nothing it answers can be attributed to it")
		}
		if seen[r.Name] {
			t.Errorf("two rules are named %q", r.Name)
		}
		seen[r.Name] = true
	}
}

// TestEveryVerdictHasItsOwnName is the same property for the verdicts, and it is what makes
// a verdict identifiable outside this package: the watch states it as a STRING in the log
// line an operator reads and in the coverage a supervisor's own tests assert over (see
// pipeline's TestEveryVerdictAUnitTestCanDriveIsReachedThroughTheRealWiring, which reads the
// verdict back out of that line). Two kinds sharing a name make one of them unobservable
// from there.
func TestEveryVerdictHasItsOwnName(t *testing.T) {
	named := map[string]Kind{}
	for _, r := range rules {
		if k, ok := named[r.Kind.String()]; ok && k != r.Kind {
			t.Errorf("verdicts %d and %d are both called %q", k, r.Kind, r.Kind.String())
		}
		named[r.Kind.String()] = r.Kind
	}
}
