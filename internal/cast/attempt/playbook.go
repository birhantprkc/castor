package attempt

import (
	"context"
	"errors"

	"github.com/stupside/castor/internal/cast/health"
)

// playbook maps fault kinds to retry strategies in cheapest-first order; switchCandidate is last.
var playbook = map[kind][]strategy{
	// Loop never reaches here (cancelled cast ends immediately).
	cancelled: nil,

	// No document/copy to blame when nothing established; re-extract for fresh signed URL is above.
	unreachable: {switchCandidate},

	// Link went silent; asked once more (reconnect ceilings; opening burst earns rate limiter silence).
	sourceStalled: {relaxRead, switchCandidate},

	// Rung is cheaper and keeps source; candidate second (source may publish one rung only).
	underDelivering: {degradeRendition, switchCandidate},

	// Try not copying first; both offered (undecodable bitstream may decode elsewhere).
	copyBrokeUpstream: {decodeAxis, switchCandidate},

	// No recovery offered (repeat attempt refused by ledger, max-attempts counter N/A here).
	rendererGone: nil,

	// Serve instead if URL refused; walk ordering on chance source is the problem.
	rendererRefused: {serveInstead, switchCandidate},

	// Switch only (not decodeAxis; lost artifact is delivery's fault, not reader's).
	producedNothing: {switchCandidate},

	// Unknown failure offers no recovery (inventing one wastes viewer's time).
	unclassified: nil,
}

// revision is value (not three returns) because nothing-offered is ordinary.
type revision struct {
	attempt  Attempt
	strategy strategy
	offered  bool
}

// revise chooses first applicable strategy from playbook that hasn't been run.
func revise(ctx context.Context, in Intent, o Outcome, f *fault, led *ledger, resolver SourceResolver) revision {
	if !revisable(o) {
		return revision{}
	}

	facts := change{intent: in, attempt: f.attempt, outcome: o, resolver: resolver}
	for _, s := range playbook[f.kind] {
		next, ok := s.apply(ctx, facts)
		if !ok {
			continue
		}
		next.Try = f.attempt.Try + 1
		if !led.admit(next) {
			continue
		}
		return revision{attempt: next, strategy: s, offered: true}
	}
	return revision{}
}

// revisable answers if attempt can be retried: phase < Playing AND (judged OR URL-refused OR timeline unreadable).
func revisable(o Outcome) bool {
	if o.Evidence.Reached >= PhasePlaying {
		return false
	}
	if judged, ok := errors.AsType[*health.Fault](o.Err); ok {
		return judged.Revise
	}
	return o.Evidence.PlayErr != nil || o.Evidence.TimelineErr != nil
}
