// Package outcome holds how a cast may end, each deciding which checks its evidence can answer.
package outcome

import (
	"fmt"
	"slices"

	"github.com/stupside/castor/e2e/judge"
)

// Plays is a cast that reached the receiver and ended cleanly: every invariant and expectation applies.
type Plays struct{}

func (Plays) Name() string { return "plays" }

func (Plays) Judge(e judge.Evidence) []string {
	return slices.Concat(
		judge.FailIf(e.CastErr != nil, fmt.Sprintf("castor failed: %v", e.CastErr)),
		judge.FailIf(!e.Handed, "castor exited cleanly but never handed the receiver anything"),
	)
}

func (Plays) Scope(invariants, expectations []judge.Check) ([]judge.Check, error) {
	return slices.Concat(invariants, expectations), nil
}
