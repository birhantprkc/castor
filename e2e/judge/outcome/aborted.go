package outcome

import (
	"slices"

	"github.com/stupside/castor/e2e/judge"
)

// Aborted is a cast that reached the receiver and then failed, castor exiting with an error rather than serving forever.
type Aborted struct{}

func (Aborted) Name() string { return "aborted" }

func (Aborted) Judge(e judge.Evidence) []string {
	return slices.Concat(
		judge.FailIf(e.CastErr == nil, "castor exited cleanly, want it to fail the cast"),
		judge.FailIf(!e.Handed, "castor never handed the receiver anything, want it to fail after the hand-off"),
	)
}

func (Aborted) Scope(_, expectations []judge.Check) ([]judge.Check, error) {
	return nil, judge.OnlyWhenPlayed(expectations)
}
