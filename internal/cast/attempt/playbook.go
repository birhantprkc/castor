package attempt

import (
	"context"
	"errors"
	"fmt"

	"github.com/stupside/castor/internal/cast/watch"
)

// playbook is what castor tries after each class of fault, in order, first strategy that
// applies. It is keyed exactly like core.deliveries: a Kind missing from it is an error
// naming the kind rather than a fall-through, because a fault nobody decided about is a
// cast that quietly stopped recovering.
//
// An empty entry is a decision and not an oversight: it says castor has nothing to offer
// for that class, so the cast is refused with the fault, the measurements and what had
// already been spent. Each one names what would fill it and what that needs, because the
// difference between "there is no recovery for this" and "nobody has written it yet" is
// exactly what a reader of this table needs to know.
//
// Order inside an entry is cheapest-first, where cheap means what a viewer pays: a change
// that keeps the link and the content castor already chose costs a few seconds, and a
// change of link costs whatever that link turns out to be. SwitchCandidate is therefore
// last in every entry it appears in, and it is in every entry where another link could
// conceivably behave differently.
var playbook = map[Kind][]Strategy{
	// Nothing, and the loop never reaches here: a cancelled cast ends with the
	// cancellation. A cast the user stopped is not a cast to start again.
	Cancelled: nil,

	// A link that established nothing is worth abandoning for the next one the ranker
	// admitted. Nothing else applies: there is no rung to drop to when no document was read,
	// no copy to blame when nothing was copied, and re-extracting for a fresh signed URL is
	// a phase above this loop.
	Unreachable: {SwitchCandidate},

	// A link that went silent is asked once more, politely, before it is abandoned: the
	// verdict fires past two reconnect ceilings, and castor spends the first ninety seconds
	// of every VOD read demanding segments as fast as the wire will carry them, which is its
	// own documented way of earning a rate limiter's silence.
	SourceStalled: {RelaxRead, SwitchCandidate},

	// The motivating failure. The rung comes first because it is the cheaper move and keeps
	// the source that was chosen for its content; the candidate comes second because a
	// source publishing one rung leaves nothing to drop to (media.Origin.Sole), which is
	// exactly the run this ordering was written for. RelaxRead is deliberately absent: a
	// link that cannot reach playback speed while ALLOWED twice it is not being held back by
	// its allowance, and lowering the allowance cannot make it faster.
	UnderDelivering: {DegradeRendition, SwitchCandidate},

	// The copy is what broke, so the first thing to try is not copying. Both are offered
	// because a bitstream this reader cannot pass through may still be one it can decode
	// (DecodeAxis), and a link whose fragments arrive truncated may simply be a worse link
	// than the next one.
	CopyBrokeUpstream: {DecodeAxis, SwitchCandidate},

	// A renderer that refused the URL it was handed is not asked to fetch again: castor
	// reads the source and serves it a local stream instead, which is the same value the
	// operator's one knob writes. A renderer that refused a stream castor was already
	// serving has nothing left here (ServeInstead answers false), so the ordering is walked
	// on the chance that the source, and not the renderer, is what it refused.
	RendererRefused: {ServeInstead, SwitchCandidate},

	// The next link, and only that. DecodeAxis is deliberately NOT offered: the artifact
	// that never appeared is the DELIVERY's, while the axes travelling in the evidence are
	// the READER's copies, so clearing them aims a re-encode of the whole title at a party
	// that was not implicated. A container refusing a track it has no stream type for is
	// prevented by the carriage table before anything runs, not recovered from afterwards.
	ProducedNothing: {SwitchCandidate},

	// Nothing, by definition. A failure nobody recognises offers no aim to take, and
	// inventing one spends a viewer's time on a cast castor has no reason to think will go
	// differently.
	Unclassified: nil,
}

// revision is what the playbook answered with: the attempt to run next and the strategy
// that produced it, or nothing offered. It is a value rather than three returns because
// "nothing was offered" is an ordinary answer here and not an error, and the log line that
// announces a revision needs the strategy that caused it.
type revision struct {
	Attempt  Attempt
	Strategy Strategy
	Offered  bool
}

// revise chooses what to try after a fault: the first strategy the playbook offers for its
// kind that both applies and produces an attempt this cast has not already run, and only
// where the failure is one that may be answered by changing the attempt at all.
func revise(ctx context.Context, in Intent, o Outcome, f *Fault, led *ledger, prog Program) (revision, error) {
	if !revisable(o) {
		return revision{}, nil
	}

	strategies, ok := playbook[f.Kind]
	if !ok {
		return revision{}, fmt.Errorf("no playbook entry for a %s fault", f.Kind)
	}

	change := Change{Intent: in, Attempt: f.Attempt, Outcome: o, Program: prog}
	for _, s := range strategies {
		next, ok := s.Apply(ctx, change)
		if !ok {
			continue
		}
		next.Try = f.Attempt.Try + 1
		if !led.admit(next) {
			continue
		}
		return revision{Attempt: next, Strategy: s, Offered: true}, nil
	}
	return revision{}, nil
}

// revisable answers whether a failed attempt may be changed and run again at all. It is a
// CONJUNCTION, and that shape is the point: both terms have to be satisfied, so a failure
// nobody characterised is abandoned rather than retried.
//
// The first term is the phase, and decision 1 is the whole of it. Nothing in castor seeks,
// each attempt owns a fresh work directory and a fresh connect, and no renderer family is
// known to accept a second Play mid-session, so a cast a viewer is already watching cannot be
// started over: it would replay the film from the beginning at minute forty.
//
// The second term is whether anybody judged the failure, and it defaults to no because the
// alternative defaults to retrying. A watch's fault carries the action table's own answer
// (watch.Fault.Revise, set from watch.actions), which is where the pre-playback windows are
// decided to be answerable. A renderer's refusal of the URL is the one failure with no watch
// behind it that a revision still answers, and there is nothing to rewind for it: the renderer
// declined the URL, so nobody was ever handed a playable stream.
//
// Everything else is a failure castor cannot aim a recovery from, and spending a viewer's time
// re-reading a whole title on it is the wrong direction for a rule whose point is never to
// restart a cast someone is watching. The failure that made this a conjunction rather than a
// phase check: a reader that died mid-title at exit 183 arrived as a bare encoder error under a
// phase that said "reading", matched the copy-broke-upstream row on that status alone, and the
// cast started over from byte zero.
func revisable(o Outcome) bool {
	if o.Reached() >= PhasePlaying {
		return false
	}
	var judged *watch.Fault
	if errors.As(o.Err, &judged) {
		return judged.Revise
	}
	return o.Evidence.PlayErr != nil
}
