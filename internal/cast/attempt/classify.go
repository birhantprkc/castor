package attempt

import (
	"fmt"
	"strings"
	"time"

	"github.com/stupside/castor/internal/cast/watch"
)

// Kind is what class of thing went wrong with an attempt. It is the key the recovery
// playbook is written against, so it names classes of cause and not symptoms: two
// failures share a Kind exactly when the same change to the attempt would answer both.
type Kind int

const (
	// Unclassified is the zero value because it is the honest answer before any row has
	// spoken. It is a real verdict and not an error arm: castor meets failures nobody has
	// characterised, and the useful response is to say so and carry the evidence rather
	// than to make a class fit and aim a recovery from it.
	Unclassified Kind = iota
	// Cancelled is the cast's own context ending. It is not a failure of anything.
	Cancelled
	// Unreachable is a link that established nothing: the read reached a terminal error
	// having landed no media and never stated a speed.
	Unreachable
	// SourceStalled is a source that stopped delivering while it was still supposed to be
	// delivering, past the point ffmpeg's own reconnects could have recovered it.
	SourceStalled
	// UnderDelivering is a source arriving slower than it will be played, so no amount of
	// patience turns it into playback.
	UnderDelivering
	// CopyBrokeUpstream is a reader that exited on packets it was passing through: the
	// bitstream it was handed cannot be copied into the container it was copying into, so
	// what answers it is to stop copying that axis rather than to read the same link again.
	CopyBrokeUpstream
	// RendererRefused is a renderer that would not play what it was pointed at, or that
	// took the URL and never came for the bytes.
	RendererRefused
	// ProducedNothing is a delivery that ended without an artifact a renderer could fetch:
	// a container refusing a track it has no stream type for at header-write time, or a
	// producer that never got an input it could read.
	ProducedNothing
)

func (k Kind) String() string {
	switch k {
	case Cancelled:
		return "cancelled"
	case Unreachable:
		return "unreachable"
	case SourceStalled:
		return "source-stalled"
	case UnderDelivering:
		return "under-delivering"
	case CopyBrokeUpstream:
		return "copy-broke-upstream"
	case RendererRefused:
		return "renderer-refused"
	case ProducedNothing:
		return "produced-nothing"
	default:
		return "unclassified"
	}
}

// classRule is one row of the classification table: what it recognises, why, and the
// class it names.
type classRule struct {
	// Name identifies the row in a log line and in the fault, so a refusal says which rule
	// read the evidence and not only what it concluded.
	Name string
	// Why is the reasoning, carried out to the caller: a cast castor gave up on has to say
	// what it thinks happened where a user can read it rather than only in this file.
	Why string
	// When reports whether this row recognises this evidence. It reads Evidence and nothing
	// else, which is what makes every class reachable from a test with no process, no
	// network and no renderer.
	When func(Evidence) bool
	// Kind is the class.
	Kind Kind
}

// classes is what castor makes of a failed attempt, in order, first match.
//
// Every row's discriminator is STRUCTURAL: a terminal state, a health verdict, a phase, or
// a party's own error. None of them reads what ffmpeg printed, and a test pins that: prose
// is not a contract (see the carriage package doc), so a wording change in ffmpeg may cost
// a message its sharpness and may never cost a cast its class. Evidence.Lines travels into
// the fault for a human to read, never into a decision.
//
// Adding a class is one row here plus one entry in the playbook plus one case in
// classify_test.go, and the two coupling tests keep the tables in agreement.
var classes = []classRule{{
	// First, because a cancellation is upstream of every symptom the rows below read:
	// castor kills the reader and the encoder, both report a broken pipe, the delivery
	// reports a severed client, and each of those would happily be named as the fault. A
	// cast the user stopped failed at nothing.
	//
	// This row is the single owner of that rule for a cast's RESULT. A stage suppressing
	// its own killed process's error is a statement about that stage's terminal state; what
	// the CAST makes of the whole wreckage is decided once, here, from the context and not
	// from anybody's error text.
	Name: "cancelled",
	Why:  "the cast was cancelled",
	When: func(e Evidence) bool { return e.Cancelled },
	Kind: Cancelled,
}, {
	// Nothing was established about this link at all: no byte landed and the reader never
	// stated a speed, so there is not even a throughput to be disappointed by. That is a
	// link to abandon rather than a cast to tune, which is why it is a class of its own and
	// not a stalled read.
	//
	// A read that landed media and THEN died is deliberately not this: what killed it is
	// still to be established, and calling that link unreachable would send a cast that was
	// working seconds ago to the recovery for one that never worked at all.
	Name: "unreachable",
	Why:  "the source read reached a terminal error having landed no media and never stated a speed, so nothing about this link was established",
	When: func(e Evidence) bool {
		return e.Verdict == watch.Dead && e.Reached <= PhaseReading && e.Health.Landed == 0 && e.Health.Samples == 0
	},
	Kind: Unreachable,
}, {
	// The reader exited on its own account while passing packets through untouched. This is
	// the failure that reached a user as "encoder: spool producer failed: upstream pull:
	// exit status 183", with the reader's own status buried inside the name of the stage
	// that read the wreckage, and the fix for it is the one thing nothing here could say:
	// which party failed at what.
	//
	// It is keyed on the READER because the reader is the process that copies into MPEG-TS,
	// and therefore the one carrying the *_mp4toannexb filter ffmpeg inserts itself. A
	// fragment abandoned mid-read is truncated, a truncated AVCC stream desynchronises that
	// filter, and it exits 183 on "Invalid NAL unit size (-1140850681 > 97253)". The
	// discriminator is structural: a POSITIVE exit status (castor kills the reader on every
	// fault it names itself, and a killed process has no status, so reading "no status" as
	// an exit would blame the copy for every stall) and at least one axis being copied (a
	// produced axis is produced to the floor, which is carriable by definition).
	//
	// The line that names the bitstream sharpens the refusal and decides nothing: the class
	// and the recovery are the same whatever ffmpeg printed, which is what keeps a wording
	// change from costing a cast (see tells and TestNoClassIsDecidedByProse).
	Name: "copy-broke-upstream",
	Why:  "the source read exited on packets it was copying, so the bitstream it was handed cannot be passed through as it is",
	When: func(e Evidence) bool {
		return e.Reached <= PhaseReading && e.ReadExit > 0 && e.Copied.Any()
	},
	Kind: CopyBrokeUpstream,
}, {
	// The verdict already waited out two reconnect ceilings before it fired, so by the time
	// this class is reached the link has had every chance ffmpeg's own retries could give
	// it. The likeliest cause is a signed playlist whose segments have expired.
	Name: "source-stalled",
	Why:  "the source stopped delivering entirely while it was still supposed to be delivering",
	When: func(e Evidence) bool { return e.Verdict == watch.Stalled },
	Kind: SourceStalled,
}, {
	// The failure this whole layer was built for. It is a fact about the LINK and not about
	// the media: the run that named it delivered 33 KB and one second of picture in thirty
	// seconds against a reader allowed twice realtime.
	Name: "under-delivering",
	Why:  "the source delivers fewer media seconds per wall-clock second than playback consumes, so the cast can never catch up however long it is given",
	When: func(e Evidence) bool { return e.Verdict == watch.Undeliverable },
	Kind: UnderDelivering,
}, {
	// Three shapes, one class, because one change answers all of them: stop asking this
	// renderer to fetch and hand it something it will take. A renderer that refused the URL
	// outright, one that accepted it and never came for the bytes (bytes_sent=0), and one that
	// took some and stopped while castor was still serving the rest are the same event caught
	// at three different moments, the last of them only after the cast ended (see
	// Evidence.Undelivered).
	Name: "renderer-refused",
	Why:  "the renderer would not play what it was pointed at, never came for the bytes, or stopped taking them with the program still being served",
	When: func(e Evidence) bool {
		return e.PlayErr != nil || e.Undelivered != nil || e.Verdict == watch.Unfetched
	},
	Kind: RendererRefused,
}, {
	// The producer ended and the artifact a renderer would have been pointed at was never
	// written. A container refuses a track it has no stream type for at header-write time,
	// before a single byte, and an ADTS AAC copy into the mp4 muxer exits 255 having
	// written audio:0KiB; a reader whose every segment answers 404 ends the same way. Both
	// are a delivery with nothing to deliver, and neither is a renderer's doing.
	Name: "produced-nothing",
	Why:  "the delivery ended without producing anything a renderer could fetch",
	When: func(e Evidence) bool { return e.Verdict == watch.Dead && e.Reached == PhaseOpening },
	Kind: ProducedNothing,
}, unclassified}

// unclassified is the total row, and it is both the table's last entry and the answer to a
// table that lost it. Naming an unrecognised failure is worth a row of its own: the fault
// still carries the phase, the measurements and the evidence, which is the whole material
// the next row is written from.
var unclassified = classRule{
	Name: "unclassified",
	Why:  "the attempt failed in a way no rule recognises",
	When: func(Evidence) bool { return true },
	Kind: Unclassified,
}

// classify names what went wrong with one attempt: the fault the loop keys its recovery
// on, and the refusal a caller reads when there is none to offer.
func classify(in Intent, a Attempt, o Outcome) *Fault {
	r := classFor(o.Evidence)
	return &Fault{
		Kind:       r.Kind,
		Why:        r.Why,
		Rule:       r.Name,
		Attempt:    a,
		Candidates: len(in.Candidates),
		Evidence:   o.Evidence,
		Err:        o.Err,
	}
}

// classFor walks the table for one attempt's evidence.
func classFor(e Evidence) classRule {
	for _, r := range classes {
		if r.When(e) {
			return r
		}
	}
	// Reachable only if the total row is edited out from under this walk. Answering with it
	// anyway is what keeps such an edit from producing a fault with no name and no reason,
	// which is the one thing worse here than an unrecognised failure.
	return unclassified
}

// Fault is a failed attempt, classified: what class of thing went wrong, why, on which
// attempt, on what evidence, and what had already been tried before it.
//
// It is the whole hand-off out of this layer. The loop keys its recovery on Kind and its
// refusal on the measurements, and it carries the failing party's own error unwrapped, so
// a cast whose reader died fails WITH that error rather than with a description of the
// stage that noticed.
type Fault struct {
	// Kind is the class, Why the row's reasoning, and Rule the row that read the evidence.
	Kind Kind
	Why  string
	Rule string

	// Attempt is what was being tried, which is half of what a failure means: the same
	// verdict on the fourth candidate at the lightest rung says something very different
	// from the same verdict on the first.
	Attempt Attempt

	// Candidates is how many links the ranker offered this cast, and it is the other half of
	// what the attempt's own index means: "candidate 2" says nothing until it is read against
	// how many there were, and a refusal on the last one has to be able to say that castor
	// spent the ordering rather than stopping early.
	Candidates int

	// Evidence is everything the attempt left behind, measurements included.
	Evidence Evidence

	// Tried names the strategies this cast already spent, in order. A refusal that lists
	// them is the difference between "castor could not cast this" and "castor tried the
	// three things it has and here is what each of them measured".
	Tried []string

	// Err is the attempt's own error, joined by the adapter in the order that attributes it.
	// It is unwrapped, so errors.Is over a cast's result still finds a cancellation or an
	// exit status rather than only this description of it.
	Err error
}

func (f *Fault) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s (try %d, %s, reached %s, %s)",
		f.Kind, f.Why, f.Attempt.Try, f.Attempt, f.Evidence.Reached, f.Evidence.Health)
	if terms := f.arithmetic(); len(terms) > 0 {
		fmt.Fprintf(&b, "; %s", strings.Join(terms, "; "))
	}
	if len(f.Tried) > 0 {
		fmt.Fprintf(&b, "; already tried: %s", strings.Join(f.Tried, ", "))
	}
	if lines := tells(f.Evidence.Lines); len(lines) > 0 {
		fmt.Fprintf(&b, "; the reader said: %s", strings.Join(lines, " | "))
	}
	if f.Err != nil {
		fmt.Fprintf(&b, ": %s", f.Err)
	}
	return b.String()
}

// arithmetic is the sentence castor already held every term of and never said. The run this
// layer was built for ended as a TV error and bytes_sent=0 while the numbers that explain it
// sat in the evidence unread: what the link measured, how long the program takes at that
// measurement, whether the source offered anything lighter, and how much of the ranker's
// ordering had been spent.
//
// Every term is skipped rather than guessed at when its input is missing, for the reason
// media.Origin.ProjectedRuntime refuses one: a source that published no duration has no
// runtime to project, and a confident number over a missing input is worse than silence.
func (f *Fault) arithmetic() []string {
	var terms []string
	o := f.Attempt.Origin
	if o.Duration > 0 {
		terms = append(terms, fmt.Sprintf("the source published a %s program", o.Duration))
	}
	if at, ok := o.ProjectedRuntime(f.Evidence.Health.Speed); ok {
		terms = append(terms, fmt.Sprintf("delivering it at the measured %.4gx takes %s",
			float64(f.Evidence.Health.Speed), at.Round(time.Minute)))
	}
	if len(o.Renditions) > 0 {
		if o.Sole() {
			terms = append(terms, "the source published one rendition, so there was nothing lighter to fall back to")
		} else {
			terms = append(terms, fmt.Sprintf("the source published %d renditions", len(o.Renditions)))
		}
	}
	if f.Candidates > 0 {
		spent := fmt.Sprintf("candidate %d of %d", f.Attempt.Candidate+1, f.Candidates)
		if f.Attempt.Candidate+1 >= f.Candidates {
			spent += fmt.Sprintf(", every one of the %d links the ranker offered", f.Candidates)
		}
		terms = append(terms, spent)
	}
	return terms
}

// tells is what the blamed party printed, quoted into the refusal so that an opaque exit
// status is actionable: "exit status 183" gives a user nothing to move on, and "Invalid NAL
// unit size" beside it says the bitstream arrived truncated.
//
// It quotes and never decides. Prose is not a contract (see the carriage package doc), so a
// wording change in ffmpeg may cost this line its sharpness and may never cost a cast its
// class, which is what TestNoClassIsDecidedByProse holds open. The tail is bounded because a
// refusal a user has to scroll is one they do not read.
func tells(lines []string) []string {
	const most = 4
	if len(lines) > most {
		return lines[len(lines)-most:]
	}
	return lines
}

func (f *Fault) Unwrap() error { return f.Err }
