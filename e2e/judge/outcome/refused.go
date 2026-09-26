package outcome

import (
	"fmt"
	"slices"

	"github.com/stupside/castor/e2e/judge"
	"github.com/stupside/castor/e2e/judge/invariant"
)

// Refused is a cast castor gives up on before handing anything over, exiting with an error.
type Refused struct{}

func (Refused) Name() string { return "refused" }

func (Refused) Judge(e judge.Evidence) []string {
	return slices.Concat(
		judge.FailIf(e.CastErr == nil, "castor exited cleanly, want it to refuse the source"),
		judge.FailIf(e.Killed, "castor never gave up and was killed at the suite's deadline, want it to refuse on its own"),
		judge.FailIf(e.Handed, fmt.Sprintf("castor handed over %s, want it to refuse before any hand-off", e.Received.URL)),
	)
}

func (Refused) Scope(_, expectations []judge.Check) ([]judge.Check, error) {
	return []judge.Check{invariant.Clean{}}, judge.OnlyWhenPlayed(expectations)
}
