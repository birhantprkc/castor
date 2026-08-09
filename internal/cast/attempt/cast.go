package attempt

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"strings"
)

// Cast runs one cast to a verdict: one attempt at a time, revising while a fault is still
// answerable and no renderer holds a URL, and refusing with the numbers when it is not.
//
// It is the whole of castor's resilience and it performs none of the cast: what an attempt
// does is the runner's business, and what to do about what happened is this loop's. That
// split is what makes a starving 4K read, a dead link and a renderer that never fetched
// three rows of a table rather than three stretches of a stage nobody could reach.
func Cast(ctx context.Context, in Intent, run Runner, prog Program) error {
	if len(in.Candidates) == 0 {
		// The one shape this loop must not discover halfway through: an index into an empty
		// ordering. A caller with nothing to attempt has a bug, and a cast that reports it as a
		// failed attempt sends whoever reads the log looking at a source instead.
		return errors.New("a cast needs at least one candidate link to attempt")
	}
	if prog == nil {
		// Refused rather than worked around, because the workaround is invisible and expensive:
		// a cast that could not ask what a link publishes would read every link past the first
		// as an unresolved document, with no height cap applied and no companion audio
		// rendition paired, and it would report success while doing it.
		return errors.New("a cast needs a way to re-read what a link publishes")
	}

	slog.InfoContext(ctx, "source program",
		"candidates", len(in.Candidates),
		"renditions", len(in.Origin.Renditions),
		"sole", in.Origin.Sole(),
		"segmented", in.Origin.Segmented,
		"segment_framing", in.Origin.Framing,
		"live", in.Origin.Live,
		"encrypted", in.Origin.Encrypted,
		"duration", in.Origin.Duration,
	)
	slog.InfoContext(ctx, "source read policy",
		"policy", in.Read.Name,
		"why", in.Read.Why,
		// A zero deadline is a term of this policy and not a missing value: one shape of
		// source is read with none at all, and this line is where that is visible before
		// anybody wonders why a stalled read took two and a half minutes to be named.
		"read_deadline", in.Read.Deadline,
		"backoff_max", in.Read.Backoff,
		"retry_statuses", in.Read.RetryStatuses,
		"segment_retries", in.Read.SegmentRetries,
		"readrate", in.Read.Pace.Realtime,
		"burst", in.Read.Pace.Burst,
	)

	led := &ledger{}
	a := in.first()
	led.admit(a)

	// The strategies this cast has spent, in order. They travel into the refusal because a
	// cast that failed after dropping two rungs and switching links has said something very
	// different from one that failed on its first try.
	var tried []string

	for {
		slog.InfoContext(ctx, "attempt", "try", a.Try, "candidates", len(in.Candidates), "shape", a.String())

		out := run.Run(ctx, a)
		if out.Err == nil {
			return nil
		}

		f := classify(in, a, out)
		f.Tried = tried
		if f.Kind == Cancelled {
			// A cast the user stopped failed at nothing, so it ends WITH the cancellation rather
			// than with a classified fault. context.Cause is preferred over the attempt's own
			// error because a concurrent stage that cancelled the run named a reason, and every
			// party downstream of the kill reports only its broken pipe.
			return cmp.Or(context.Cause(ctx), out.Err)
		}

		rev, err := revise(ctx, in, out, f, led, prog)
		if err != nil {
			// A class the playbook was never told about. The cast fails with both, because the
			// fault is what a user has to act on and the missing entry is what a contributor does.
			return errors.Join(f, err)
		}
		if !rev.Offered {
			// The arithmetic is on this line as well as in the error, because the two are read in
			// different places: a user reads the returned error, and whoever is handed the log
			// after the fact reads this. Both have to answer "why did castor stop trying".
			slog.WarnContext(ctx, "cast refused",
				"verdict", f.Kind.String(),
				"rule", f.Rule,
				"why", f.Why,
				"reached", out.Reached().String(),
				"health", f.Evidence.Health.String(),
				"candidate", a.Candidate+1,
				"candidates", len(in.Candidates),
				"arithmetic", strings.Join(f.arithmetic(), "; "),
				"tried", tried,
			)
			return f
		}

		slog.WarnContext(ctx, "revising the cast",
			"verdict", f.Kind.String(),
			"why", f.Why,
			"strategy", rev.Strategy.Name,
			"expecting", rev.Strategy.Why,
			"health", f.Evidence.Health.String(),
			"next", rev.Attempt.String(),
		)
		tried = append(tried, rev.Strategy.Name)
		a = rev.Attempt
	}
}

// ledger is every attempt this cast has already run.
//
// It is the backstop under the strategies' own monotonicity, not a substitute for it: each
// of them moves the attempt strictly down a finite order, so a repeat means a table edit
// broke that, and the honest response is to stall the loop and refuse rather than to run
// the same attempt for as long as the source keeps failing it the same way.
type ledger struct{ ran map[string]bool }

// admit records an attempt and reports whether it is one this cast has not run before.
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
