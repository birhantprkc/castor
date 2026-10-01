// Package attempt: cast spine (attempts, faults, recovery) via pure logic.
package attempt

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/stupside/castor/internal/source"
)

// Runner executes attempt returning Outcome, joining read, judgement, and delivery accounts.
type Runner interface {
	Run(ctx context.Context, a Attempt) Outcome
}

// SourceResolver resolves link answering on copy (preserves ordering) and whole rung (preserves codec info).
type SourceResolver interface {
	RefetchProgram(ctx context.Context, s *source.Stream, chosen source.Rendition) (source.Resolution, error)
}

// Cast runs cast to verdict, revising attempts while faults answerable, refusing when not.
func Cast(ctx context.Context, in Intent, run Runner, resolver SourceResolver) error {
	if err := validate(in, resolver); err != nil {
		return err
	}
	a, ok := nextReadable(ctx, in, resolver, Attempt{Try: 1, candidate: -1, Delivery: in.Delivery})
	if !ok {
		return fmt.Errorf("none of the %d links could be resolved", len(in.Candidates))
	}
	announce(ctx, in, a)

	led := &ledger{}
	led.admit(a)

	// Track tried strategies to show failure pattern and recovery path.
	var tried []string

	for {
		slog.InfoContext(ctx, "attempt", "try", a.Try, "candidates", len(in.Candidates), "shape", a.String())

		out := run.Run(ctx, a)
		// Attribute error to runner before cancellation check.
		out.Err = attribute(out.Err, out.Evidence.ReadErr)
		if out.Err == nil {
			return nil
		}

		f := classify(in, a, out, tried)
		if f.kind == cancelled {
			// Prefer context.Cause (cancellation reason) over attempt error (pipe break).
			return cmp.Or(context.Cause(ctx), out.Err)
		}

		rev := revise(ctx, in, out, f, led, resolver)
		if !rev.offered {
			refused(ctx, in, a, out, f, tried)
			return f
		}

		revising(ctx, f, rev)
		tried = append(tried, rev.strategy.name)
		a = rev.attempt
	}
}

func validate(in Intent, resolver SourceResolver) error {
	if len(in.Candidates) == 0 {
		// Return error not log to pinpoint caller bug, not source issue.
		return errors.New("a cast needs at least one candidate link to attempt")
	}
	for i, candidate := range in.Candidates {
		if candidate == nil || candidate.URL == nil {
			return fmt.Errorf("cast candidate %d has no URL", i)
		}
	}
	if resolver == nil {
		// Resolver required: prevents silent failures with unresolved links.
		return errors.New("a cast needs a way to re-read what a link publishes")
	}
	return nil
}

func announce(ctx context.Context, in Intent, a Attempt) {
	slog.InfoContext(ctx, "source program",
		"candidates", len(in.Candidates),
		"candidate", a.candidate+1,
		"renditions", len(a.Origin.Renditions),
		"sole", a.Origin.Sole(),
		"segmented", a.Origin.Segmented,
		"segment_framing", a.Origin.Framing,
		"live", a.Origin.Live,
		"duration", a.Origin.Duration,
	)
	primaryRead := a.Fetch.Primary(a.Program)
	slog.InfoContext(ctx, "source read policy",
		"policy", primaryRead.Name,
		"why", primaryRead.Why,
		"inputs", a.Fetch.String(),
		// Zero deadline is valid for some sources, not missing.
		"read_deadline", primaryRead.Deadline,
		"segment_retries", primaryRead.SegmentRetries,
		"readrate", primaryRead.Pace.Realtime,
		"burst", primaryRead.Pace.Burst,
	)
}

func refused(ctx context.Context, in Intent, a Attempt, out Outcome, f *fault, tried []string) {
	// Arithmetic in both error and log for user readability.
	slog.WarnContext(ctx, "cast refused",
		"verdict", f.kind.String(),
		"rule", f.rule,
		"why", f.why,
		"reached", out.Evidence.Reached.String(),
		"health", f.evidence.Health.String(),
		"candidate", a.candidate+1,
		"candidates", len(in.Candidates),
		"arithmetic", strings.Join(f.arithmetic(), "; "),
		"tried", tried,
	)
}

func revising(ctx context.Context, f *fault, rev revision) {
	slog.WarnContext(ctx, "revising the cast",
		"verdict", f.kind.String(),
		"why", f.why,
		"strategy", rev.strategy.name,
		"expecting", rev.strategy.why,
		"health", f.evidence.Health.String(),
		"next", rev.attempt.String(),
	)
}

// ledger tracks attempts to backstop strategy monotonicity; repeats indicate table edit errors.
type ledger struct{ ran map[string]bool }

// admit records attempt if unseen; returns false if duplicate.
func (l *ledger) admit(a Attempt) bool {
	key := a.key()
	if l.ran[key] {
		return false
	}
	if l.ran == nil {
		l.ran = make(map[string]bool)
	}
	l.ran[key] = true
	return true
}
