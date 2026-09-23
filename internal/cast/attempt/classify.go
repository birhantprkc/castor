package attempt

import (
	"fmt"
	"strings"
	"time"

	"github.com/stupside/castor/internal/cast/policy/watch"
)

// Kind classifies attempt failures: two failures share a Kind exactly when the same change would answer both.
type Kind int

const (
	// Unclassified is a real verdict before any row has spoken, not an error.
	Unclassified Kind = iota
	// Cancelled is the cast's context ending, not a failure.
	Cancelled
	// Unreachable is a link that established no media and never stated a speed.
	Unreachable
	// SourceStalled is a source that stopped delivering mid-cast.
	SourceStalled
	// UnderDelivering is a source arriving slower than it will be played.
	UnderDelivering
	// CopyBrokeUpstream is a reader that exited on packets it was copying.
	CopyBrokeUpstream
	// RendererGone is a renderer that crashed or switched off, not one that refused.
	RendererGone
	// RendererRefused is a renderer that would not play the URL or never came for bytes.
	RendererRefused
	// ProducedNothing is a delivery that ended without an artifact to fetch.
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
	case RendererGone:
		return "renderer-gone"
	case RendererRefused:
		return "renderer-refused"
	case ProducedNothing:
		return "produced-nothing"
	default:
		return "unclassified"
	}
}

type classRule struct {
	Name string
	Why  string
	When func(Evidence) bool
	Kind Kind
}

type table struct {
	rules []classRule
	total classRule
}

// classes is the ordered classification rules for failed attempts; every discriminator is STRUCTURAL.
var classes = table{rules: []classRule{{
	// First, because cancellation is upstream of every symptom below; decided from context, not error text.
	Name: "cancelled",
	Why:  "the cast was cancelled",
	When: func(e Evidence) bool { return e.Cancelled },
	Kind: Cancelled,
}, {
	Name: "renderer-gone",
	Why:  "the renderer stopped answering the protocol it was being watched over, so there is nothing at the far end of this cast to send anything to",
	When: func(e Evidence) bool { return e.RendererGone != nil },
	Kind: RendererGone,
}, {
	// Media landed then died is deliberately not unreachable; that would send a working cast to recovery.
	Name: "unreachable",
	Why:  "the source read reached a terminal error having landed no media and never stated a speed, so nothing about this link was established",
	When: func(e Evidence) bool {
		return e.Verdict == watch.Dead && e.Reached <= PhaseReading && e.Health.Landed == 0 && e.Health.Samples == 0
	},
	Kind: Unreachable,
}, {
	// Keyed on READER exit status and copied axes; castor kills the reader on its own faults.
	Name: "copy-broke-upstream",
	Why:  "the source read exited on packets it was copying, so the bitstream it was handed cannot be passed through as it is",
	When: func(e Evidence) bool {
		return e.Reached <= PhaseReading && e.ReadExit > 0 && e.Copied.Any()
	},
	Kind: CopyBrokeUpstream,
}, {
	// One change (stop asking renderer to fetch) answers all three shapes at different moments.
	Name: "renderer-refused",
	Why:  "the renderer would not play what it was pointed at, never came for the bytes, or stopped taking them with the program still being served",
	When: func(e Evidence) bool {
		return e.PlayErr != nil || e.Undelivered != nil || e.Verdict == watch.Unfetched
	},
	Kind: RendererRefused,
}, {
	// Container refuses tracks at header-write time or reader's every segment answered 404.
	Name: "produced-nothing",
	Why:  "the delivery ended without producing anything a renderer could fetch",
	When: func(e Evidence) bool { return e.Verdict == watch.Dead && e.Reached == PhaseOpening },
	Kind: ProducedNothing,
}}, total: unclassified}

var verdictClasses = map[watch.Kind]classRule{
	// It already waited out two reconnect ceilings.
	watch.Stalled: {
		Name: "source-stalled",
		Why:  "the source stopped delivering entirely while it was still supposed to be delivering",
		Kind: SourceStalled,
	},
	// The failure this whole layer was built for; a fact about the LINK, not media.
	watch.Undeliverable: {
		Name: "under-delivering",
		Why:  "the source delivers fewer media seconds per wall-clock second than playback consumes, so the cast can never catch up however long it is given",
		Kind: UnderDelivering,
	},
}

// unclassified is the fallback row; fault still carries phase, measurements and evidence.
var unclassified = classRule{
	Name: "unclassified",
	Why:  "the attempt failed in a way no rule recognises",
	Kind: Unclassified,
}

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

func classFor(e Evidence) classRule {
	for _, r := range classes.rules {
		if r.When(e) {
			return r
		}
	}
	if r, ok := verdictClasses[e.Verdict]; ok {
		return r
	}
	return classes.total
}

type Fault struct {
	Kind Kind
	Why  string
	Rule string
	// Attempt is half of what failure means; verdict varies by candidate rank.
	Attempt Attempt
	// Candidates is the other half; refusal on the last one means castor spent the whole ordering.
	Candidates int
	Evidence   Evidence
	// Tried distinguishes 'could not cast this' from 'tried three things and here is what each measured'.
	Tried []string
	// Err is the attempt's own error, unwrapped so errors.Is still finds cancellation or exit status.
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
	if gone := f.Evidence.RendererGone; gone != nil {
		fmt.Fprintf(&b, "; the renderer %q stopped answering", gone.Renderer)
	}
	if lines := tells(f.Evidence.Lines); len(lines) > 0 {
		fmt.Fprintf(&b, "; the reader said: %s", strings.Join(lines, " | "))
	}
	if f.Err != nil {
		fmt.Fprintf(&b, ": %s", f.Err)
	}
	return b.String()
}

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

// tells quotes what the blamed party printed; never decides (see TestNoClassIsDecidedByProse).
func tells(lines []string) []string {
	const most = 4
	if len(lines) > most {
		return lines[len(lines)-most:]
	}
	return lines
}

func (f *Fault) Unwrap() error { return f.Err }
