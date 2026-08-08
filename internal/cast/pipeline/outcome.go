package pipeline

import (
	"context"
	"errors"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/watch"
)

// landing is what one composition reports about itself: how far it got, what it ended with,
// and separately what the parties whose failure is not the same statement as the cast's left
// behind.
//
// The read is held apart from the cast's error for the reason the attempt layer exists: a
// dead upstream used to reach a user through the encoder's stdin as "encoder: spool producer
// failed: upstream pull: exit status 183", blaming the process that merely noticed. Its own
// error and its own stderr are kept here so the join can lead with them.
type landing struct {
	reached attempt.Phase
	err     error

	// readErr and readLines are the source read's own terminal error and retained stderr, on
	// the compositions that run a reader of their own. One whose reader IS its encoder leaves
	// them nil: there is one party there, and the delivery already reports it.
	readErr   error
	readLines []string

	// readExit is the status that read exited with and readCopied the axes it was passing
	// through untouched. Together they are what makes a broken copy nameable rather than
	// merely fatal: a positive status is a read that failed at something it was doing, and
	// the copied axes are what it was doing. A leg with no reader of its own leaves the
	// status at zero, which reads as no exit to blame.
	readExit   int
	readCopied carriage.Axes

	// playErr is the renderer's own refusal of the URL, on the composition that asks it to
	// play itself rather than through the delivery driver.
	playErr error
}

// outcome folds the landing and the judgement that ended it into the one value the loop
// reads.
//
// Three joins happen here, and each of them is a rule with exactly one home. Whether the
// cast was cancelled is read from the context, so it holds however the killed parties word
// their broken pipes. How far the cast got is the LATER of what the leg saw and what the
// verdict's own window says, because the two answer about different things: a leg reports the
// stages it drove and the Play the delivery told it about, while a verdict reports the window
// it was reached in, which is the only account of an artifact gate the leg never sees inside.
// And the read's error leads the cast's error, because the party that failed is the party to
// name.
func (l landing) outcome(ctx context.Context) attempt.Outcome {
	e := attempt.Evidence{
		Reached:   l.reached,
		Cancelled: ctx.Err() != nil,
		ReadErr:   l.readErr,
		ReadExit:  l.readExit,
		Copied:    l.readCopied,
		PlayErr:   l.playErr,
		Lines:     l.readLines,
	}

	// Read off the error rather than carried as a field of the landing: the statement is the
	// DELIVERY's, made after the sink's own Wait had already ended cleanly, so the leg that
	// called Serve never holds it as a fact of its own.
	var undelivered *core.Undelivered
	if errors.As(l.err, &undelivered) {
		e.Undelivered = undelivered
	}

	var verdict *watch.Fault
	judged := errors.As(l.err, &verdict)
	if judged {
		e.Verdict, e.Health = verdict.Kind, verdict.Health
		e.Reached = max(e.Reached, phaseOf(verdict.Window))
		if len(e.Lines) == 0 {
			e.Lines = verdict.Evidence
		}
	}

	return attempt.Outcome{Err: attribute(l.err, l.readErr, judged), Evidence: e}
}

// attribute decides whose account of a failure the cast reports.
//
// A verdict is left alone: it already names the party it blames and carries that party's own
// error inside it. Otherwise, where the cast's error is nothing but the read's error under
// the name of the stage that noticed it ("encoder: spool producer failed: upstream pull: exit
// status 183" is the reader's own 183, wrapped twice on its way through a buffer and an
// encoder's stdin), the read's error replaces it. Where the delivery has something of its own
// to say (its exit status, a marker that means its output is not playable), both are
// reported, the read first.
func attribute(err, readErr error, judged bool) error {
	switch {
	case readErr == nil || judged:
		return err
	case errors.Is(err, readErr):
		return readErr
	default:
		return errors.Join(readErr, err)
	}
}

// phaseOf reads how far a cast had got off the window a verdict was reached in. It is the
// same statement made from the other side: a watch judging a read nobody is watching yet is a
// cast that has reached PhaseReading and no further, and one judging a renderer that holds a
// URL is a cast past the point of changing its mind.
func phaseOf(w watch.Window) attempt.Phase {
	switch w {
	case watch.Opening:
		return attempt.PhaseOpening
	case watch.Playing:
		return attempt.PhasePlaying
	default:
		return attempt.PhaseReading
	}
}
