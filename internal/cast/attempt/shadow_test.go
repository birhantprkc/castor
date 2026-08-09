package attempt

import (
	"errors"
	"iter"
	"reflect"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/watch"
)

// This file holds the missing mirror of the two coupling tests. Those assert that every class
// the table REACHES has a playbook entry and that no entry answers a class no row reaches;
// nothing asserted that every ROW can be reached. A row shadowed by one above it fails
// nothing: the walk answers with the row above, both coupling tests still pass, and the row
// reads as a recognised failure with a recovery aimed at it while the recovery that actually
// runs is the neighbour's.
//
// The property is first-match, per row, over the cross product of the evidence a finished
// attempt leaves behind.

// evidenceFact is one thing an attempt leaves behind, at the values the table can tell apart.
type evidenceFact struct {
	// name is the Evidence field, which is what a failure has to name to be actionable.
	name string
	// values each write one value of this fact into an Evidence.
	values []func(*Evidence)
}

// refused and abandoned are the two parties' own errors as a row reads them: only their
// presence is ever read, because prose is not a contract (see TestNoClassIsDecidedByProse).
var (
	refused   = errors.New("SOAP SetAVTransportURI: 714")
	died      = errors.New("upstream pull: exit status 183")
	abandoned = &core.Undelivered{}
)

// evidenceFacts is the state space every row is searched over: the cross product, not a list
// of cases, because a shadowed row is exactly one no case anybody thought to write reaches.
//
// Every verdict is included rather than the four the rows name, so a row keyed on a verdict
// that cannot coexist with its other clauses is caught rather than assumed.
var evidenceFacts = []evidenceFact{{
	name: "Cancelled",
	values: []func(*Evidence){
		func(e *Evidence) { e.Cancelled = false },
		func(e *Evidence) { e.Cancelled = true },
	},
}, {
	name: "Verdict",
	values: []func(*Evidence){
		func(e *Evidence) { e.Verdict = watch.Starting },
		func(e *Evidence) { e.Verdict = watch.Ready },
		func(e *Evidence) { e.Verdict = watch.Healthy },
		func(e *Evidence) { e.Verdict = watch.Stalled },
		func(e *Evidence) { e.Verdict = watch.Undeliverable },
		func(e *Evidence) { e.Verdict = watch.Dead },
		func(e *Evidence) { e.Verdict = watch.Unfetched },
	},
}, {
	name: "Reached",
	values: []func(*Evidence){
		func(e *Evidence) { e.Reached = PhaseUnstarted },
		func(e *Evidence) { e.Reached = PhaseReading },
		func(e *Evidence) { e.Reached = PhaseOpening },
		func(e *Evidence) { e.Reached = PhasePlaying },
		func(e *Evidence) { e.Reached = PhaseDelivered },
	},
}, {
	// The measurements as whole readings rather than one field at a time, because that is what a
	// watch hands over: a landed byte with no stated speed and a stated speed with nothing landed
	// are both real, and telling them apart is the whole of what separates a link that
	// established nothing from a cast that failed at something else.
	name: "Health",
	values: []func(*Evidence){
		func(e *Evidence) { e.Health = watch.Health{} },
		func(e *Evidence) { e.Health = watch.Health{Landed: 33088} },
		func(e *Evidence) { e.Health = watch.Health{Samples: 4} },
		func(e *Evidence) {
			e.Health = watch.Health{Landed: 33088, Position: time.Second, Speed: 0.159, Headroom: 2, Samples: 12}
		},
	},
}, {
	name: "ReadErr",
	values: []func(*Evidence){
		func(e *Evidence) { e.ReadErr = nil },
		func(e *Evidence) { e.ReadErr = died },
	},
}, {
	// Three values, because the sign is the contract: negative is no status to read (castor
	// killed the reader), zero is a clean exit, positive is a reader that failed at something
	// it was doing.
	name: "ReadExit",
	values: []func(*Evidence){
		func(e *Evidence) { e.ReadExit = -1 },
		func(e *Evidence) { e.ReadExit = 0 },
		func(e *Evidence) { e.ReadExit = 183 },
	},
}, {
	name: "Copied",
	values: []func(*Evidence){
		func(e *Evidence) { e.Copied = carriage.Axes{} },
		func(e *Evidence) { e.Copied = carriage.Axes{Video: true} },
		func(e *Evidence) { e.Copied = carriage.Axes{Video: true, Audio: true} },
	},
}, {
	name: "PlayErr",
	values: []func(*Evidence){
		func(e *Evidence) { e.PlayErr = nil },
		func(e *Evidence) { e.PlayErr = refused },
	},
}, {
	name: "Undelivered",
	values: []func(*Evidence){
		func(e *Evidence) { e.Undelivered = nil },
		func(e *Evidence) { e.Undelivered = abandoned },
	},
}}

// evidences walks the cross product of the facts above.
func evidences() iter.Seq[Evidence] {
	return func(yield func(Evidence) bool) {
		at := make([]int, len(evidenceFacts))
		for {
			var e Evidence
			for i, f := range evidenceFacts {
				f.values[at[i]](&e)
			}
			if !yield(e) {
				return
			}
			i := len(at) - 1
			for ; i >= 0; i-- {
				at[i]++
				if at[i] < len(evidenceFacts[i].values) {
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

// TestEveryClassRuleIsTheFirstMatchForSomeEvidence is the shadowing property. A row that is
// never the first match names a class nothing is ever classified as, so the playbook entry
// beside it is a recovery that never runs, and the table reads as covering a failure it hands
// to the row above instead.
//
// A row shadowed by a neighbour and a row whose clauses cannot all hold at once are reported
// apart, because the fixes are opposite: one is an ordering, the other is a clause.
func TestEveryClassRuleIsTheFirstMatchForSomeEvidence(t *testing.T) {
	var (
		won     = map[string]bool{}
		matched = map[string]bool{}
		lost    = map[string]string{}
	)
	for e := range evidences() {
		first := classFor(e)
		won[first.Name] = true
		for _, r := range classes {
			if r.Name == first.Name || !r.When(e) {
				continue
			}
			matched[r.Name] = true
			// The first winner and not the last, so the row named is the one a reader will
			// find answering the shadowed row's own case rather than whichever state the
			// walk happened to end on.
			if _, ok := lost[r.Name]; !ok {
				lost[r.Name] = first.Name
			}
		}
	}

	for _, r := range classes {
		switch {
		case won[r.Name]:
		case matched[r.Name]:
			t.Errorf("rule %q never classifies an attempt: every evidence it recognises is taken by %q above it, so its %s playbook entry is a recovery that never runs",
				r.Name, lost[r.Name], r.Kind)
		default:
			t.Errorf("rule %q recognises no evidence at all, so it can never fire: its clauses cannot hold together", r.Name)
		}
	}
}

// TestEveryClassRuleHasItsOwnName pins what the property above is keyed on, and what a
// refusal is read by: two rows sharing a name make one of them invisible to every assertion
// here and to anybody reading which rule read the evidence out of a cast castor gave up on.
func TestEveryClassRuleHasItsOwnName(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range classes {
		if r.Name == "" {
			t.Error("a classification rule carries no name, so nothing it classifies can be attributed to it")
		}
		if seen[r.Name] {
			t.Errorf("two classification rules are named %q", r.Name)
		}
		seen[r.Name] = true
	}
}

// TestTheEvidenceSpaceCoversEverythingAnAttemptLeavesBehind is what keeps the search above
// honest as Evidence grows. A field the space holds still is a field no row can be shown to be
// shadowed over: a rule keyed on a newly added one would be searched over a space where it is
// always its zero value, and the search would report the row as reachable on the strength of
// never having varied the thing it reads.
//
// Lines is the one exemption, and it is a rule rather than a gap: no row may DECIDE on what a
// party printed, because prose is not a contract and a wording change may cost a message its
// sharpness and never a cast its class (see TestNoClassIsDecidedByProse). Varying it here would
// be building a space for a rule that must not exist.
func TestTheEvidenceSpaceCoversEverythingAnAttemptLeavesBehind(t *testing.T) {
	varied := map[string]bool{"Lines": true}
	for _, f := range evidenceFacts {
		if len(f.values) < 2 {
			t.Errorf("fact %q is written with fewer than two values, so nothing about it is ever varied", f.name)
		}
		varied[f.name] = true
	}
	for field := range reflect.TypeFor[Evidence]().NumField() {
		if name := reflect.TypeFor[Evidence]().Field(field).Name; !varied[name] {
			t.Errorf("Evidence.%s is never varied by the state space, so no row can be caught shadowed over it", name)
		}
	}
}
