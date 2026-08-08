package attempt

import (
	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/watch"
)

// Phase is how far one attempt got. It is the axis every recovery is gated on, and the
// only one: a fault reached before a renderer holds a URL may still be answered by
// changing the attempt, and one reached after it may only be attributed.
//
// It is ordered, and the order is the safety property. Nothing seeks in castor, each
// attempt owns a fresh buffer, and no renderer family is known to accept a second Play
// mid-session, so a cast a viewer is already watching cannot be started over: replaying a
// film from the beginning at minute forty is worse than a clear error.
type Phase int

const (
	// PhaseUnstarted is an attempt that ended before anything was read: a work directory
	// that could not be made, or a renderer that refused the URL on a cast that reads
	// nothing itself.
	PhaseUnstarted Phase = iota
	// PhaseReading is the source read landing media with no renderer pointed at anything
	// yet.
	PhaseReading
	// PhaseOpening is the artifact a renderer will be pointed at being produced: the first
	// byte of a stream, or the playlist of a segmented directory.
	PhaseOpening
	// PhasePlaying is the renderer holding the URL. This is the line a recovery does not
	// cross.
	PhasePlaying
	// PhaseDelivered is the cast having run its course.
	PhaseDelivered
)

func (p Phase) String() string {
	switch p {
	case PhaseReading:
		return "reading"
	case PhaseOpening:
		return "opening"
	case PhasePlaying:
		return "playing"
	case PhaseDelivered:
		return "delivered"
	default:
		return "unstarted"
	}
}

// Outcome is what one attempt did, as the two things the loop needs: whether it worked,
// and the material to say why not.
type Outcome struct {
	// Err is what the attempt failed with, nil when the cast ran its course. It is the JOIN
	// of the parties that can end a cast, in the order that attributes it rather than in
	// the order they noticed: the read that lands the media first, then whatever the
	// delivery has of its own to add. Returning only the delivery's account is how a
	// source that exited 183 mid-copy was reported as "encoder: spool producer failed".
	Err error

	// Evidence is what the attempt left behind, and the only material a classification
	// rule reads.
	Evidence Evidence
}

// Reached is how far this attempt got, which is what decides whether the loop is still
// allowed to change its mind.
func (o Outcome) Reached() Phase { return o.Evidence.Reached }

// Evidence is what one finished attempt left behind: measurements, terminal states and
// the parties' own errors, never a decision. A classification rule reads this and nothing
// else, which is what keeps the whole table exercisable over hand-built values.
type Evidence struct {
	// Reached is how far the attempt got before it ended.
	Reached Phase

	// Cancelled reports that the cast's own context ended, which is upstream of every
	// other symptom here: castor kills the reader and the encoder, both report a broken
	// pipe, and the delivery reports a severed client. It is read from the context and
	// never from an error's text, so the rule holds however those parties word it.
	Cancelled bool

	// Verdict is the verdict a health rule reached about this attempt, and Health the
	// measurements it was reached on. watch.Starting means no watch reached one, which is
	// the honest answer for a cast that failed before anything was judged.
	Verdict watch.Kind
	Health  watch.Health

	// ReadErr is the source read's own terminal error, unwrapped: the party a
	// classification most often blames, and the one whose error used to reach a user under
	// the encoder's name. It is nil on a leg that runs no reader of its own.
	ReadErr error

	// ReadExit is the exit status that error came with, and it is the structural
	// discriminator behind a broken copy: a reader that exited 183 mid-stream failed at
	// something it was doing, where a reader castor killed failed at nothing.
	//
	// Zero is a clean exit and NEGATIVE is no status to read (the process was killed, or
	// has not been waited on), so a rule about a failure must require a POSITIVE one. That
	// distinction is the whole of it: castor kills the reader on every fault it names
	// itself, so reading "no status" as an exit would blame the copy for every stall.
	ReadExit int

	// Copied is the axes that reader was passing through untouched when it ended, which is
	// the other half of the same discriminator: only a copied axis can die on a bitstream
	// filter, since a produced one is produced to the floor and the floor is carriable by
	// definition. It is also exactly what a recovery has to stop doing.
	Copied carriage.Axes

	// PlayErr is the renderer's own refusal of the URL it was handed, on the legs that ask
	// it to play themselves. A renderer that took the URL and then never fetched it is not
	// this: that is a verdict above, reached by watching whether anybody came for the bytes.
	PlayErr error

	// Undelivered is the delivery's own account of a cast that ran its course while the
	// renderer stopped taking the stream (see core.Undelivered), which is a statement about
	// the whole cast rather than a state anything could be caught in: nothing distinguishes a
	// paused viewer from a renderer that went away while it is quiet, and the arithmetic that
	// does becomes available only once the cast has ended.
	//
	// It is the last account of the "exited 0 having cast nothing" family: every party here
	// can report success while the film reached nobody, and this is the one that cannot.
	Undelivered error

	// Lines is the retained stderr of the party being blamed, which is what a human reads
	// when no rule recognised what happened. No rule DECIDES on it: prose is not a
	// contract, so a line may sharpen the message a refusal carries (see tells) and may
	// never decide a class or aim a recovery.
	Lines []string
}
