package attempt

import (
	"context"
	"errors"

	"github.com/stupside/castor/internal/cast/watch"
)

// playbook maps fault kinds to retry strategies in cheapest-first order; switchCandidate is last.
var playbook = map[Kind][]strategy{
	// Loop never reaches here (cancelled cast ends immediately).
	Cancelled: nil,

	// No document/copy to blame when nothing established; re-extract for fresh signed URL is above.
	Unreachable: {switchCandidate},

	// Link went silent; asked once more (reconnect ceilings; opening burst earns rate limiter silence).
	SourceStalled: {relaxRead, switchCandidate},

	// Rung is cheaper and keeps source; candidate second (source may publish one rung only).
	UnderDelivering: {degradeRendition, switchCandidate},

	// Try not copying first; both offered (undecodable bitstream may decode elsewhere).
	CopyBrokeUpstream: {decodeAxis, switchCandidate},

	// No recovery offered (repeat attempt refused by ledger, max-attempts counter N/A here).
	RendererGone: nil,

	// Serve instead if URL refused; walk ordering on chance source is the problem.
	RendererRefused: {serveInstead, switchCandidate},

	// Switch only (not decodeAxis; lost artifact is delivery's fault, not reader's).
	ProducedNothing: {switchCandidate},

	// Unknown failure offers no recovery (inventing one wastes viewer's time).
	Unclassified: nil,
}

// revision is value (not three returns) because nothing-offered is ordinary.
type revision struct {
	Attempt  Attempt
	Strategy strategy
	Offered  bool
}

// revise chooses first applicable strategy from playbook that hasn't been run.
func revise(ctx context.Context, in Intent, o Outcome, f *Fault, led *ledger, resolver SourceResolver) revision {
	if !revisable(o) {
		return revision{}
	}

	facts := change{Intent: in, Attempt: f.Attempt, Outcome: o, Resolver: resolver}
	for _, s := range playbook[f.Kind] {
		next, ok := s.Apply(ctx, facts)
		if !ok {
			continue
		}
		next.Try = f.Attempt.Try + 1
		if !led.admit(next) {
			continue
		}
		return revision{Attempt: next, Strategy: s, Offered: true}
	}
	return revision{}
}

// revisable answers if attempt can be retried: phase < Playing AND (judged OR URL-refused OR timeline unreadable).
func revisable(o Outcome) bool {
	if o.Evidence.Reached >= PhasePlaying {
		return false
	}
	if judged, ok := errors.AsType[*watch.Fault](o.Err); ok {
		return judged.Revise
	}
	return o.Evidence.PlayErr != nil || o.Evidence.TimelineErr != nil
}
