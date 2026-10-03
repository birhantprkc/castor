package recovery

import (
	"context"
	"errors"

	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
)

// playbook is each fault kind's recoveries, cheapest first; switching candidate always comes last.
var playbook = map[kind][]strategy{
	// Nothing about this link was established, so nothing on it is worth changing.
	unreachable: {switchCandidate},

	// A link that went silent may have rate-limited the opening burst, so it is asked once more at playback pace.
	sourceStalled: {relaxRead, switchCandidate},

	// Decoding comes first; a bitstream this one cannot copy may still copy from another link.
	copyBrokeUpstream: {decodeAxis, switchCandidate},

	// Nothing at the far end to send anything to.
	deviceGone: nil,

	// A device refusing the URL is served instead; the next link, in case the source is the problem.
	deviceRefused: {serveInstead, switchCandidate},

	// The delivery lost the artifact, not the reader, so decoding would answer nothing.
	producedNothing: {switchCandidate},

	// An unknown failure offers no recovery: inventing one wastes the viewer's time.
	unclassified: nil,
}

// revision is the attempt a strategy offers; offering nothing is ordinary.
type revision struct {
	attempt  Attempt
	strategy strategy
	offered  bool
}

// revise is the first strategy in the playbook that offers an attempt not run yet.
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

// revisable is an attempt that failed before playing, judged revisable, refused by the device, or on an unreadable timeline.
func revisable(o Outcome) bool {
	if o.Evidence.Reached >= health.Playing {
		return false
	}
	if judged, ok := errors.AsType[*health.Fault](o.Err); ok {
		return judged.Revise
	}
	return o.Evidence.PlayErr != nil || o.Evidence.TimelineErr != nil
}
