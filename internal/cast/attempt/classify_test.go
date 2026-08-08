package attempt

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/watch"
)

// classCase is one row of the classification asserted over a hand-built Evidence: no
// process, no network, no renderer, which is the whole reason Evidence is a value.
type classCase struct {
	name string
	why  string
	in   Evidence
	want Kind
}

// classCases covers every row of the table, both arms of the rows that have two, and the
// near misses that would shadow a neighbouring row if a clause were dropped.
var classCases = []classCase{{
	name: "a cancelled cast is not a failed cast",
	why:  "castor kills the reader and the encoder, so every party below reports a broken pipe and any of them could be named instead",
	in:   Evidence{Cancelled: true, Reached: PhaseReading, Verdict: watch.Undeliverable},
	want: Cancelled,
}, {
	name: "a link that landed nothing and never stated a speed established nothing",
	why:  "there is not even a throughput to be disappointed by, so what is wrong is the link and not the cast",
	in:   Evidence{Reached: PhaseReading, Verdict: watch.Dead},
	want: Unreachable,
}, {
	name: "a read that died having landed media is not called unreachable",
	why:  "it was working seconds ago, so what killed it is still to be established and the recovery for a dead link would be aimed at the wrong thing",
	in:   Evidence{Reached: PhaseReading, Verdict: watch.Dead, Health: watch.Health{Landed: 33088}},
	want: Unclassified,
}, {
	name: "a read that died having stated a speed is not called unreachable",
	why:  "a reader that reported its pace was measured, which is exactly what this class says did not happen",
	in:   Evidence{Reached: PhaseReading, Verdict: watch.Dead, Health: watch.Health{Samples: 4}},
	want: Unclassified,
}, {
	name: "the observed AVCC desync is a copy that broke upstream",
	why:  "the reader exited 183 on packets it was passing through, which is a fact about the bitstream and not about the link, the delivery or the renderer",
	in: Evidence{Reached: PhaseReading, Verdict: watch.Dead, ReadExit: 183,
		Copied: carriage.Axes{Video: true, Audio: true},
		Health: watch.Health{Landed: 4 << 20, Speed: 1.8, Headroom: 2, Samples: 12}},
	want: CopyBrokeUpstream,
}, {
	name: "a reader castor killed is not a copy that broke",
	why:  "castor kills the reader on every fault it names itself, so a missing exit status must never read as an exit: this shape is a stall, and blaming the copy would re-encode a title for nothing",
	in: Evidence{Reached: PhaseReading, Verdict: watch.Stalled, ReadExit: -1,
		Copied: carriage.Axes{Video: true, Audio: true},
		Health: watch.Health{Landed: 4 << 20}},
	want: SourceStalled,
}, {
	name: "a reader that exited while copying nothing is not a copy that broke",
	why:  "a produced axis is produced to the floor, which the buffer's container carries by definition, so there was no passed-through bitstream to blame",
	in: Evidence{Reached: PhaseReading, Verdict: watch.Dead, ReadExit: 183,
		Health: watch.Health{Landed: 4 << 20, Samples: 12}},
	want: Unclassified,
}, {
	name: "a reader that exited cleanly is not a copy that broke",
	why:  "exit 0 is a read that finished, and a cast that failed around it failed at something else",
	in: Evidence{Reached: PhaseReading, Verdict: watch.Dead, ReadExit: 0,
		Copied: carriage.Axes{Video: true},
		Health: watch.Health{Landed: 4 << 20, Samples: 12}},
	want: Unclassified,
}, {
	name: "a copy that broke after the renderer was playing is not offered to the recovery",
	why:  "the phase is part of the discriminator because nothing can be done about it there: the buffer keeps serving, and a reader's death past the gate truncates a tail rather than starting a cast over",
	in: Evidence{Reached: PhasePlaying, Verdict: watch.Dead, ReadExit: 183,
		Copied: carriage.Axes{Video: true},
		Health: watch.Health{Landed: 4 << 20, Samples: 12}},
	want: Unclassified,
}, {
	name: "a source that stopped delivering is stalled",
	why:  "the verdict only fires past two reconnect ceilings, so the link has had every retry ffmpeg could give it",
	in:   Evidence{Reached: PhaseReading, Verdict: watch.Stalled, Health: watch.Health{Landed: 33088}},
	want: SourceStalled,
}, {
	name: "the observed run's measurements are under-delivering",
	why:  "0.159x against a reader allowed 2x is a link that can never catch up, whatever it has landed so far",
	in: Evidence{Reached: PhaseReading, Verdict: watch.Undeliverable,
		Health: watch.Health{Landed: 33088, Speed: 0.159, Headroom: 2, Samples: 4}},
	want: UnderDelivering,
}, {
	name: "a renderer that refused the URL is a renderer refusing",
	why:  "castor read nothing and produced nothing on that leg, so there is no other party to blame",
	in:   Evidence{PlayErr: errors.New("SOAP SetAVTransportURI: 714")},
	want: RendererRefused,
}, {
	name: "a renderer that took the URL and never fetched it is the same class",
	why:  "one change answers both: stop asking this renderer to fetch and hand it something it takes",
	in:   Evidence{Reached: PhasePlaying, Verdict: watch.Unfetched},
	want: RendererRefused,
}, {
	name: "a renderer that took some of the stream and stopped is the same class",
	why:  "a cast that ran its course having handed the renderer a fraction of what it produced delivered the program to nobody, which is the same event as one that never fetched",
	in: Evidence{Reached: PhasePlaying, Health: watch.Health{Requests: 1},
		Undelivered: &core.Undelivered{Handed: 2 * time.Minute, Produced: 2 * time.Hour, Playing: time.Hour}},
	want: RendererRefused,
}, {
	name: "a delivery whose artifact never appeared produced nothing",
	why:  "a container refuses a track it has no stream type for at header-write time, before a single byte, and a reader whose segments all 404 ends the same way",
	in:   Evidence{Reached: PhaseOpening, Verdict: watch.Dead},
	want: ProducedNothing,
}, {
	name: "a failure nothing recognises is named as such",
	why:  "the fault still carries the phase, the measurements and the evidence, which is the material the next row is written from",
	in:   Evidence{Reached: PhaseReading},
	want: Unclassified,
}}

func TestClassify(t *testing.T) {
	for _, tt := range classCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := classFor(tt.in).Kind; got != tt.want {
				t.Errorf("classified %s, want %s: %s", got, tt.want, tt.why)
			}
		})
	}
}

// TestUnclassifiedIsReachable pins the property that makes the table total rather than
// exhaustive: a failure nobody characterised is a verdict castor can reach and report,
// not an error arm and not a zero value.
func TestUnclassifiedIsReachable(t *testing.T) {
	if got := classFor(Evidence{}).Kind; got != Unclassified {
		t.Errorf("an evidence nothing recognises classified %s, want %s", got, Unclassified)
	}
	if !slices.ContainsFunc(classes, func(r classRule) bool { return r.Kind == Unclassified }) {
		t.Error("the total row is not in the table, so an unrecognised failure has no row to name it")
	}
	// The row itself, because the walk answers with it whether or not it matched: that
	// fallback exists so a table edit cannot produce a fault with no name, and it would
	// otherwise hide a total row that had stopped being total.
	for _, tt := range classCases {
		if !unclassified.When(tt.in) {
			t.Errorf("the total row does not answer %q, so it is not total", tt.name)
		}
	}
}

// TestNoClassIsDecidedByProse is the discipline this table shares with carriage: prose is
// not a contract. A marker or a stderr tail may sharpen the message a user reads and may
// never decide which class a failure is, or a wording change in ffmpeg costs a cast its
// recovery.
func TestNoClassIsDecidedByProse(t *testing.T) {
	prose := []string{
		"[hls @ 0x] Invalid NAL unit size (2054458 > 21367)",
		"Server returned 404 Not Found",
		"AAC bitstream not in ADTS format and extradata missing",
	}
	for _, tt := range classCases {
		t.Run(tt.name, func(t *testing.T) {
			want := classFor(tt.in).Kind

			said := tt.in
			said.Lines = prose
			if got := classFor(said).Kind; got != want {
				t.Errorf("classified %s once the reader had printed something, want %s whatever it printed", got, want)
			}

			silent := tt.in
			silent.Lines = nil
			if got := classFor(silent).Kind; got != want {
				t.Errorf("classified %s with nothing printed, want %s: a silent process is not a different failure", got, want)
			}
		})
	}
}

// TestEveryClassTheTableReachesHasAPlaybookEntry is the coupling between the two tables.
// A class with no entry is a fault castor quietly stopped recovering from, which is the
// failure mode a map lookup hides and this test exists to make impossible.
func TestEveryClassTheTableReachesHasAPlaybookEntry(t *testing.T) {
	for _, r := range classes {
		if _, ok := playbook[r.Kind]; !ok {
			t.Errorf("rule %q reaches %s, which the playbook was never told about", r.Name, r.Kind)
		}
	}
}

// TestNoPlaybookEntryIsOfferedForAClassNoRuleReaches is the mirror, and it keeps the
// playbook describing the program rather than an intention: a recovery aimed at a class
// nothing can classify is a strategy nobody will ever see run.
func TestNoPlaybookEntryIsOfferedForAClassNoRuleReaches(t *testing.T) {
	for kind := range playbook {
		if !slices.ContainsFunc(classes, func(r classRule) bool { return r.Kind == kind }) {
			t.Errorf("the playbook answers %s, which no classification rule reaches", kind)
		}
	}
}

// TestEveryClassHasItsOwnName guards the one hole a String method with a default arm
// leaves: a new class that nobody added a case for reads as the default one, so a log
// line, a refusal and a ledger key would all name the wrong thing.
func TestEveryClassHasItsOwnName(t *testing.T) {
	seen := map[string]Kind{}
	for _, r := range classes {
		name := r.Kind.String()
		if other, ok := seen[name]; ok && other != r.Kind {
			t.Errorf("%s and %s both print as %q", r.Kind, other, name)
		}
		seen[name] = r.Kind
	}
}

// TestEveryRuleCarriesItsReasoning pins that a refusal can always say what castor thinks
// happened: the Why is what reaches a user, and a row with none reports a class name and
// nothing to act on.
func TestEveryRuleCarriesItsReasoning(t *testing.T) {
	for _, r := range classes {
		if r.Name == "" || r.Why == "" {
			t.Errorf("rule %+v has no name or no reasoning", r)
		}
	}
}

// TestFaultCarriesTheFailingPartysOwnError is what keeps a classification from replacing
// a failure with a description of it: a cast whose reader exited 183 must still answer
// errors.Is for that exit status, and its message must carry the measurements the refusal
// is being made on.
func TestFaultCarriesTheFailingPartysOwnError(t *testing.T) {
	exit := &exec.ExitError{}
	readErr := fmt.Errorf("upstream pull: %w", exit)

	a := Attempt{Try: 2, Candidate: 1}
	f := classify(Intent{}, a, Outcome{
		Err: readErr,
		Evidence: Evidence{
			Reached: PhaseReading,
			Verdict: watch.Undeliverable,
			ReadErr: readErr,
			Health:  watch.Health{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: 4},
		},
	})
	f.Tried = []string{"degrade-rendition"}

	if !errors.Is(f, exit) {
		t.Error("the fault does not unwrap to the reader's own exit status, so nothing above it can recognise one")
	}
	msg := f.Error()
	for _, want := range []string{"under-delivering", "speed=0.0627", "headroom=2", "already tried: degrade-rendition", "upstream pull"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal %q does not mention %q", msg, want)
		}
	}
}
