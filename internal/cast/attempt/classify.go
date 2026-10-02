package attempt

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/stupside/castor/internal/cast/health"
)

// kind classifies attempt failures: two failures share a kind exactly when the same change would answer both.
type kind int

const (
	// unclassified is a real verdict before any row has spoken, not an error.
	unclassified kind = iota
	// cancelled is the cast's context ending, not a failure.
	cancelled
	// unreachable is a link that established no media and never stated a speed.
	unreachable
	// sourceStalled is a source that stopped delivering mid-cast.
	sourceStalled
	// underDelivering is a source arriving slower than it will be played.
	underDelivering
	// copyBrokeUpstream is a reader that exited on packets it was copying.
	copyBrokeUpstream
	// rendererGone is a renderer that crashed or switched off, not one that refused.
	rendererGone
	// rendererRefused is a renderer that would not play the URL or never came for bytes.
	rendererRefused
	// producedNothing is a delivery that ended without an artifact to fetch.
	producedNothing
)

func (k kind) String() string {
	switch k {
	case cancelled:
		return "cancelled"
	case unreachable:
		return "unreachable"
	case sourceStalled:
		return "source-stalled"
	case underDelivering:
		return "under-delivering"
	case copyBrokeUpstream:
		return "copy-broke-upstream"
	case rendererGone:
		return "renderer-gone"
	case rendererRefused:
		return "renderer-refused"
	case producedNothing:
		return "produced-nothing"
	default:
		return "unclassified"
	}
}

type classRule struct {
	// name is the rule's own name where it differs from its Kind's.
	name string
	why  string
	when func(Evidence) bool
	kind kind
}

// classes is the ordered classification rules for failed attempts; every discriminator is STRUCTURAL.
var classes = []classRule{{
	// First, because cancellation is upstream of every symptom below; decided from context, not error text.
	why:  "the cast was cancelled",
	when: func(e Evidence) bool { return e.Cancelled },
	kind: cancelled,
}, {
	why:  "the renderer stopped answering the protocol it was being watched over, so there is nothing at the far end of this cast to send anything to",
	when: func(e Evidence) bool { return e.RendererGone != nil },
	kind: rendererGone,
}, {
	// Only castor reads such a timeline, so no other policy on this link reads it either.
	name: "timeline-unreadable",
	why:  "castor could not read the timeline of a source it follows itself, so this link cannot be read at all",
	when: func(e Evidence) bool { return e.TimelineErr != nil },
	kind: unreachable,
}, {
	// Media landed then died is deliberately not unreachable; that would send a working cast to recovery.
	why: "the source read reached a terminal error having landed no media and never stated a speed, so nothing about this link was established",
	when: func(e Evidence) bool {
		return e.Verdict == health.Dead && e.Reached <= PhaseReading && e.Health.Landed == 0 && e.Health.Samples == 0
	},
	kind: unreachable,
}, {
	// A cold or failing edge truncates or drops segments; a second read of the same link may get them whole.
	name: "source-incomplete",
	why:  "the source read ended short of what the source declared, with segments truncated or skipped by the origin",
	when: func(e Evidence) bool { return e.Reached <= PhaseReading && e.ReadIncomplete },
	kind: sourceStalled,
}, {
	// Keyed on READER exit status and copied axes; castor kills the reader on its own faults.
	why: "the source read exited on packets it was copying, so the bitstream it was handed cannot be passed through as it is",
	when: func(e Evidence) bool {
		return e.Reached <= PhaseReading && e.ReadExit > 0 && e.Copied.Any()
	},
	kind: copyBrokeUpstream,
}, {
	// One change (stop asking renderer to fetch) answers all three shapes at different moments.
	why: "the renderer would not play what it was pointed at, never came for the bytes, or stopped taking them with the program still being served",
	when: func(e Evidence) bool {
		return e.PlayErr != nil || e.Undelivered != nil || e.Verdict == health.Unfetched
	},
	kind: rendererRefused,
}, {
	// Container refuses tracks at header-write time or reader's every segment answered 404; or it fell silent over a buffer already proven.
	why: "the delivery ended, or went silent over castor's own proven buffer, without producing anything a renderer could fetch",
	when: func(e Evidence) bool {
		return e.Reached == PhaseOpening && (e.Verdict == health.Dead || e.Verdict == health.Stalled && e.Buffered)
	},
	kind: producedNothing,
}}

var verdictClasses = map[health.Kind]classRule{
	// It already waited out two reconnect ceilings.
	health.Stalled: {
		why:  "the source stopped delivering entirely while it was still supposed to be delivering",
		kind: sourceStalled,
	},
	// The failure this whole layer was built for; a fact about the LINK, not media.
	health.Undeliverable: {
		why:  "the source delivers fewer media seconds per wall-clock second than playback consumes, so the cast can never catch up however long it is given",
		kind: underDelivering,
	},
}

// unrecognised is the fallback row; fault still carries phase, measurements and evidence.
var unrecognised = classRule{
	why:  "the attempt failed in a way no rule recognises",
	kind: unclassified,
}

// classify names what failed, after the strategies already tried.
func classify(in Intent, a Attempt, o Outcome, tried []string) *fault {
	r := classFor(o.Evidence)
	return &fault{
		kind:       r.kind,
		why:        r.why,
		rule:       cmp.Or(r.name, r.kind.String()),
		attempt:    a,
		candidates: len(in.Candidates),
		evidence:   o.Evidence,
		tried:      tried,
		err:        o.Err,
	}
}

func classFor(e Evidence) classRule {
	for _, r := range classes {
		if r.when(e) {
			return r
		}
	}
	if r, ok := verdictClasses[e.Verdict]; ok {
		return r
	}
	return unrecognised
}

type fault struct {
	kind kind
	why  string
	rule string
	// attempt is half of what failure means; verdict varies by candidate rank.
	attempt Attempt
	// candidates is the other half; refusal on the last one means castor spent the whole ordering.
	candidates int
	evidence   Evidence
	// tried distinguishes 'could not cast this' from 'tried three things and here is what each measured'.
	tried []string
	// err is the attempt's own error, unwrapped so errors.Is still finds cancellation or exit status.
	err error
}

func (f *fault) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s (try %d, %s, reached %s, %s)",
		f.kind, f.why, f.attempt.Try, f.attempt, f.evidence.Reached, f.evidence.Health)
	if terms := f.arithmetic(); len(terms) > 0 {
		fmt.Fprintf(&b, "; %s", strings.Join(terms, "; "))
	}
	if len(f.tried) > 0 {
		fmt.Fprintf(&b, "; already tried: %s", strings.Join(f.tried, ", "))
	}
	if gone := f.evidence.RendererGone; gone != nil {
		fmt.Fprintf(&b, "; the renderer %q stopped answering", gone.Renderer)
	}
	if lines := tells(f.evidence.Lines); len(lines) > 0 {
		fmt.Fprintf(&b, "; the reader said: %s", strings.Join(lines, " | "))
	}
	if f.err != nil {
		fmt.Fprintf(&b, ": %s", f.err)
	}
	return b.String()
}

func (f *fault) arithmetic() []string {
	var terms []string
	o := f.attempt.Origin
	if o.Duration > 0 {
		terms = append(terms, fmt.Sprintf("the source published a %s program", o.Duration))
	}
	if at, ok := o.ProjectedRuntime(f.evidence.Health.Speed); ok {
		terms = append(terms, fmt.Sprintf("delivering it at the measured %.4gx takes %s",
			float64(f.evidence.Health.Speed), at.Round(time.Minute)))
	}
	if len(o.Renditions) > 0 {
		if o.Sole() {
			terms = append(terms, "the source published one rendition, so there was nothing lighter to fall back to")
		} else {
			terms = append(terms, fmt.Sprintf("the source published %d renditions", len(o.Renditions)))
		}
	}
	spent := fmt.Sprintf("candidate %d of %d", f.attempt.candidate+1, f.candidates)
	if f.attempt.candidate+1 >= f.candidates {
		spent += fmt.Sprintf(", every one of the %d links the ranker offered", f.candidates)
	}
	terms = append(terms, spent)
	return terms
}

// tells quotes what the blamed party printed; it never decides a class.
func tells(lines []string) []string {
	const most = 4
	if len(lines) > most {
		return lines[len(lines)-most:]
	}
	return lines
}

func (f *fault) Unwrap() error { return f.err }
