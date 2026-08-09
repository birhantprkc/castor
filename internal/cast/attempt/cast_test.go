package attempt

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// scriptedRunner is the fake behind the one outbound port: it answers each attempt with
// the next outcome in its script and keeps the attempts it was handed.
//
// It is why this port exists. Every recovery path is a table row here, with no ffmpeg, no
// fixtures and no renderer, where the executor's own suite needs two real processes and
// skips entirely on a host with no ffmpeg on PATH.
type scriptedRunner struct {
	outcomes []Outcome
	seen     []Attempt
}

func (r *scriptedRunner) Run(_ context.Context, a Attempt) Outcome {
	r.seen = append(r.seen, a)
	if len(r.seen) > len(r.outcomes) {
		// A loop that ran further than the test scripted is the failure most of these cases
		// are about, so it is reported as an unrecognised failure (which no strategy answers)
		// rather than by panicking inside a fake.
		return Outcome{Err: errors.New("the loop ran an attempt nothing was scripted for")}
	}
	return r.outcomes[len(r.seen)-1]
}

var _ Runner = (*scriptedRunner)(nil)

// fakeProgram is the source layer as a recovery reaches it: what one link publishes, keyed
// on the link, with a record of what was asked.
//
// A link it holds no answer for publishes nothing and is handed back unchanged, which is
// the honest answer for a whole file (there is no document to read) and the same posture
// resolution takes toward a playlist it could not fetch.
type fakeProgram struct {
	answers map[string]published
	asked   []string
}

// published is what one link's documents said: the rung to read, the ladder behind it, and
// which rung that was.
type published struct {
	url    string
	origin media.Origin
	rung   media.Rendition
	err    error
}

func (p *fakeProgram) Refetch(_ context.Context, s *media.Stream) (*media.Stream, media.Origin, media.Rendition, error) {
	p.asked = append(p.asked, s.URL.String())
	answer, ok := p.answers[s.URL.String()]
	switch {
	case !ok:
		return s, media.Origin{}, media.Rendition{}, nil
	case answer.err != nil:
		return nil, media.Origin{}, media.Rendition{}, answer.err
	}
	// A copy, as the production adapter answers with: the ordering is the record of what a
	// cast tried, so narrowing a master must not rewrite the link the ranker published.
	link := *s
	if answer.url != "" {
		u, err := url.Parse(answer.url)
		if err != nil {
			panic(err)
		}
		link.URL = u
	}
	return &link, answer.origin, answer.rung, nil
}

var _ Program = (*fakeProgram)(nil)

// judged is the outcome a watch reaching one verdict produces, as the executor folds it: the
// verdict, the phase it was reached in, the measurements behind it, and the watch's own fault
// AS THE ATTEMPT'S ERROR.
//
// That last part is not decoration. The fault is what says whether the attempt may still be
// changed (watch.Fault.Revise, set from watch.actions), so a helper handing back a bare error
// would exercise a revision the production path cannot produce, and that is exactly how a
// failure past the playback gate came to be retried with nothing in this suite objecting.
func judged(k watch.Kind, reached Phase, h watch.Health) Outcome {
	return Outcome{
		Err: &watch.Fault{
			Kind:    k,
			Why:     "the watch ended this attempt",
			Subject: "the cast under watch",
			Window:  windowOf(reached),
			Revise:  reached < PhasePlaying,
			Health:  h,
		},
		Evidence: Evidence{Reached: reached, Verdict: k, Health: h},
	}
}

// windowOf is the window a watch that reached this phase was in, which is the same
// correspondence the executor's own fold reads in the other direction: a watch judging a read
// nobody is watching yet is a cast that got as far as reading, and one judging a renderer that
// holds a URL is a cast past the point of changing its mind.
//
// Pairing it with Revise on the phase alone is what the shipped action table says: every
// pre-playback window is answered by a revision and every playing one by abandoning, which
// watch.TestNoPlayingVerdictCanRevise holds open on that side.
func windowOf(reached Phase) watch.Window {
	switch {
	case reached >= PhasePlaying:
		return watch.Playing
	case reached == PhaseOpening:
		return watch.Opening
	default:
		return watch.BeforePlay
	}
}

// delivered is a cast that ran its course.
var delivered = Outcome{Evidence: Evidence{Reached: PhaseDelivered}}

// starving is what the observed run measured: 33 KB and one second of media in thirty
// seconds against a reader allowed twice realtime.
var starving = watch.Health{Landed: 33088, Position: time.Second, Speed: 0.159, Headroom: 2, Samples: 4}

// link is one candidate the ranker admitted, carrying the headers it was measured with so
// a strategy that rewrites the URL can be seen to keep them.
func link(t *testing.T, raw string) *media.Stream {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &media.Stream{
		URL:         u,
		ContentType: media.HLS,
		Headers:     http.Header{"Referer": {"https://player.example/"}},
	}
}

// ladder is a source that published several rungs, in publication order.
func ladder(t *testing.T, rungs ...media.Rendition) media.Origin {
	t.Helper()
	return media.Origin{Renditions: rungs, Segmented: true, Duration: 2 * time.Hour}
}

func rung(t *testing.T, raw string, bitrate media.Bitrate, height int) media.Rendition {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return media.Rendition{URL: u, Bitrate: bitrate, Height: height}
}

// policy is the read policy a segmented source of unknown framing gets, so a test's
// intent is built exactly the way the composition root builds one.
func policy(t *testing.T, o media.Origin) read.Policy {
	t.Helper()
	p := read.For(read.ShapeOf(o), 30*time.Second)
	return p
}

// TestCastRunsExactlyOneAttemptWhenNothingCanBeRevised is the shape the composition root
// builds today: one candidate, and a source layer that narrowed a master to one URL
// without saying which rung it took.
//
// Both recoveries are inapplicable for want of material rather than by a limit inside the
// loop: there is no further link in the ordering, and a rung whose declared rate never
// travelled offers no ceiling to measure a lighter one against. So the cast is one attempt
// and the answer is the refusal, which is what makes this loop's arrival change no
// behaviour at all.
func TestCastRunsExactlyOneAttemptWhenNothingCanBeRevised(t *testing.T) {
	origin := ladder(t,
		rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160),
		rung(t, "https://cdn.example/1080.m3u8", 3000000, 1080),
	)
	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/2160.m3u8")},
		Origin:     origin,
		Read:       policy(t, origin),
		Deadline:   30 * time.Second,
	}
	run := &scriptedRunner{outcomes: []Outcome{judged(watch.Undeliverable, PhaseReading, starving)}}

	err := Cast(t.Context(), in, run, &fakeProgram{})

	if len(run.seen) != 1 {
		t.Fatalf("ran %d attempts, want exactly 1: nothing about this intent can be revised", len(run.seen))
	}
	var f *Fault
	if !errors.As(err, &f) {
		t.Fatalf("cast error = %v, want a fault", err)
	}
	if f.Kind != UnderDelivering {
		t.Errorf("fault kind = %s, want %s", f.Kind, UnderDelivering)
	}
	if !strings.Contains(err.Error(), "speed=0.159") {
		t.Errorf("the refusal %q does not carry the measurement it was reached on", err)
	}
	if len(f.Tried) != 0 {
		t.Errorf("the refusal claims to have tried %v, having tried nothing", f.Tried)
	}
}

// TestCastDegradesToTheHeaviestRungTheLinkCarried is the recovery armed by the source
// layer stating which rung it read. The ceiling is measured and not guessed: 6941 kb/s
// delivering 0.159x is a link carrying about 1.1 Mbit/s, so the 3 Mbit/s rung is skipped
// and the 1 Mbit/s one is read.
func TestCastDegradesToTheHeaviestRungTheLinkCarried(t *testing.T) {
	top := rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160)
	mid := rung(t, "https://cdn.example/1080.m3u8", 3000000, 1080)
	low := rung(t, "https://cdn.example/720.m3u8", 1000000, 720)
	origin := ladder(t, top, mid, low)

	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/2160.m3u8")},
		Origin:     origin,
		// The rung the source layer narrowed to, which is what supplies the ceiling below.
		Rendition: top,
		Read:      policy(t, origin),
		Deadline:  30 * time.Second,
	}
	run := &scriptedRunner{outcomes: []Outcome{judged(watch.Undeliverable, PhaseReading, starving), delivered}}
	if err := Cast(t.Context(), in, run, &fakeProgram{}); err != nil {
		t.Fatalf("cast: %v", err)
	}

	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2", len(run.seen))
	}
	second := run.seen[1]
	if second.Rendition.Bitrate != low.Bitrate {
		t.Errorf("degraded to %d bit/s, want the heaviest rung under the measured ceiling (%d)",
			second.Rendition.Bitrate, low.Bitrate)
	}
	if second.Source.URL.String() != low.URL.String() {
		t.Errorf("second attempt reads %s, want the degraded rung %s", second.Source.URL, low.URL)
	}
	if second.Source.Headers.Get("Referer") == "" {
		t.Error("the degraded attempt lost the headers the link was measured with, so it will read a source it can no longer open")
	}
	// The measured height described the rung that just failed, so carrying it onto a lighter
	// rung is a stale measurement of a taller picture, and it is what the cast's height ceiling
	// would then judge this attempt by (see core.Shape.Height).
	if second.Source.Height != low.Height {
		t.Errorf("the degraded attempt carries height %d, want the rung it moved to (%d)", second.Source.Height, low.Height)
	}
	if run.seen[0].Source.URL.String() != top.URL.String() {
		t.Error("degrading rewrote the candidate the first attempt read, so the ordering no longer says what was tried")
	}
	if second.Try != 2 {
		t.Errorf("second attempt is try %d, want 2", second.Try)
	}
}

// TestCastWillNotDegradeAnAlreadyLightRung pins the strict-descent property that keeps the
// loop from spinning: a read that fell short of playback while running ahead of realtime
// measures a ceiling ABOVE its own rung, and the move is still down or not at all.
func TestCastWillNotDegradeAnAlreadyLightRung(t *testing.T) {
	low := rung(t, "https://cdn.example/720.m3u8", 1000000, 720)
	origin := ladder(t, rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160), low)

	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/720.m3u8")},
		Origin:     origin,
		Rendition:  low,
		Read:       policy(t, origin),
		Deadline:   30 * time.Second,
	}
	// A speed above 1 measures a ceiling of 1.4 Mbit/s over a 1 Mbit/s rung, which the
	// whole ladder above is heavier than.
	run := &scriptedRunner{outcomes: []Outcome{judged(watch.Undeliverable, PhaseReading,
		watch.Health{Landed: 4 << 20, Speed: 1.4, Headroom: 2, Samples: 4})}}
	if err := Cast(t.Context(), in, run, &fakeProgram{}); err == nil {
		t.Fatal("the cast reported success though its only attempt failed")
	}
	if len(run.seen) != 1 {
		t.Fatalf("ran %d attempts, want 1: there is no rung below the one that failed", len(run.seen))
	}
}

// TestCastSwitchesToTheNextLinkTheRankerAdmitted is the load-bearing recovery on the run
// this layer was written for: the source published one rendition, so there was no rung to
// drop to, and the ranker had reported four alternatives, two of which probed cleanly.
//
// The new link is read AS THE SOURCE LAYER RESOLVES IT, and every assertion below is about
// that. A master handed to the reader unresolved is not a smaller version of a resolved
// one: the demuxer picks a variant with no height ceiling applied, nothing pairs a
// separately published audio rendition, and no document has said how the segments are
// framed, so a cast escaping a 4K rung that could not deliver reaches for another one.
func TestCastSwitchesToTheNextLinkTheRankerAdmitted(t *testing.T) {
	sole := media.Origin{
		Renditions: []media.Rendition{rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160)},
		Segmented:  true,
		Framing:    media.FramingOutOfBand,
	}
	in := Intent{
		Candidates: []*media.Stream{
			link(t, "https://cdn.example/2160.m3u8"),
			link(t, "https://other.example/master.m3u8"),
		},
		Origin:    sole,
		Rendition: sole.Renditions[0],
		Read:      policy(t, sole),
		Deadline:  30 * time.Second,
	}
	// What the second link's own documents say: a ladder, narrowed under the cap to its 720
	// rung, whose segments carry their configuration in band.
	ladder720 := ladder(t,
		rung(t, "https://other.example/1080.m3u8", 3000000, 1080),
		rung(t, "https://other.example/720.m3u8", 1200000, 720))
	ladder720.Framing = media.FramingInBand
	prog := &fakeProgram{answers: map[string]published{
		"https://other.example/master.m3u8": {
			url:    "https://other.example/720.m3u8",
			origin: ladder720,
			rung:   ladder720.Renditions[1],
		},
	}}
	run := &scriptedRunner{outcomes: []Outcome{judged(watch.Undeliverable, PhaseReading, starving), delivered}}

	if err := Cast(t.Context(), in, run, prog); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2", len(run.seen))
	}
	if !slices.Equal(prog.asked, []string{"https://other.example/master.m3u8"}) {
		t.Errorf("the source layer was asked about %v, want the one link the cast moved onto: the others are links most casts never read", prog.asked)
	}

	second := run.seen[1]
	if second.Candidate != 1 {
		t.Errorf("second attempt reads candidate %d, want 1", second.Candidate)
	}
	if got := second.Source.URL.String(); got != "https://other.example/720.m3u8" {
		t.Errorf("second attempt reads %s, want the rung the source layer narrowed to", got)
	}
	if in.Candidates[1].URL.String() != "https://other.example/master.m3u8" {
		t.Error("resolving the new link rewrote the ordering, so it no longer records what was published")
	}
	if second.Source.Headers.Get("Referer") == "" {
		t.Error("the switched attempt lost the headers its link was measured with")
	}
	if second.Rendition.Bitrate != 1200000 || len(second.Origin.Renditions) != 2 {
		t.Errorf("second attempt carries rung %d bit/s over %d renditions, want the NEW link's own ladder and rung: a degrade after this measures a ceiling against it",
			second.Rendition.Bitrate, len(second.Origin.Renditions))
	}
	if second.Read.Name != "segment-in-band" {
		t.Errorf("second attempt reads on %q, want the policy the new link's OWN framing calls for", second.Read.Name)
	}
	if second.Read.Deadline != in.Deadline {
		t.Errorf("re-derived read deadline = %s, want the configured %s", second.Read.Deadline, in.Deadline)
	}

	// A different link is a different bitstream, so what a copy of the last one broke on is no
	// evidence against this one: carrying it across would decode a whole title on the strength
	// of another link's packets.
	blamed := Change{
		Intent:  in,
		Attempt: Attempt{Candidate: 0, Source: in.Candidates[0], Decode: carriage.Axes{Video: true}},
		Program: prog,
	}
	switched, ok := SwitchCandidate.Apply(t.Context(), blamed)
	if !ok {
		t.Fatal("the ordering offered a second link and the switch declined it")
	}
	if switched.Decode.Any() {
		t.Errorf("the switched attempt decodes %s on the strength of the previous link's packets", switched.Decode)
	}
}

// TestASwitchedLinkWhoseDocumentsCannotBeReadIsAttemptedWhole keeps a failed refetch from
// costing the recovery. It is the posture the source layer already takes toward a playlist
// it could not fetch: the reader that follows carries headers, reconnects and minutes that
// one GET does not, so what the failure costs is the facts and not the cast.
func TestASwitchedLinkWhoseDocumentsCannotBeReadIsAttemptedWhole(t *testing.T) {
	in := Intent{
		Candidates: []*media.Stream{
			link(t, "https://cdn.example/one.m3u8"),
			link(t, "https://other.example/two.m3u8"),
		},
		Deadline: 30 * time.Second,
	}
	prog := &fakeProgram{answers: map[string]published{
		"https://other.example/two.m3u8": {err: errors.New("fetching playlist: HTTP 403")},
	}}
	// A link that established nothing, whose only recovery is the next link: nothing was
	// measured to relax the read against and no document was read to offer a rung.
	dead := judged(watch.Dead, PhaseReading, watch.Health{})
	run := &scriptedRunner{outcomes: []Outcome{dead, delivered}}

	if err := Cast(t.Context(), in, run, prog); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2: an unreadable document is not a reason to abandon a link", len(run.seen))
	}
	second := run.seen[1]
	if second.Source.URL.String() != "https://other.example/two.m3u8" {
		t.Errorf("second attempt reads %s, want the link as the ranker published it", second.Source.URL)
	}
	if len(second.Origin.Renditions) != 0 || second.Rendition.Bitrate != 0 {
		t.Errorf("second attempt claims facts (%+v, %+v) about a document nobody read", second.Origin, second.Rendition)
	}
}

// TestCastNeverRevisesOnceARendererIsPlaying is decision 1 as a property of the loop: past
// the point a renderer holds a URL there is nothing to go back to. Nothing seeks, each
// attempt owns a fresh buffer, and replaying a film from the beginning at minute forty is
// worse than a clear error.
func TestCastNeverRevisesOnceARendererIsPlaying(t *testing.T) {
	for _, kind := range []watch.Kind{watch.Undeliverable, watch.Stalled, watch.Unfetched} {
		t.Run(kind.String(), func(t *testing.T) {
			in := Intent{
				Candidates: []*media.Stream{
					link(t, "https://cdn.example/one.m3u8"),
					link(t, "https://other.example/two.m3u8"),
				},
				Deadline: 30 * time.Second,
			}
			run := &scriptedRunner{outcomes: []Outcome{judged(kind, PhasePlaying, starving), delivered}}

			if err := Cast(t.Context(), in, run, &fakeProgram{}); err == nil {
				t.Fatal("the cast reported success though a viewer's playback was ended by a fault")
			}
			if len(run.seen) != 1 {
				t.Fatalf("ran %d attempts, want 1: a cast someone is watching is never started over", len(run.seen))
			}
		})
	}
}

// TestAPlayingCastIsNotRevisedEvenByAFaultThatClaimsItCanBe pins the phase term of that rule
// on its own, and it is why the loop reads the phase rather than taking the fault's word for
// it. watch.Fault.Revise is set from an action table, no shipped row reaches a revisable
// verdict in the playing window, and this is what keeps an edit to that table from being able
// to restart a film at minute forty.
func TestAPlayingCastIsNotRevisedEvenByAFaultThatClaimsItCanBe(t *testing.T) {
	in := Intent{
		Candidates: []*media.Stream{
			link(t, "https://cdn.example/one.m3u8"),
			link(t, "https://other.example/two.m3u8"),
		},
		Deadline: 30 * time.Second,
	}
	claimed := Outcome{
		Err: &watch.Fault{
			Kind: watch.Stalled, Window: watch.Playing, Revise: true,
			Subject: "the playing cast", Health: starving,
		},
		Evidence: Evidence{Reached: PhasePlaying, Verdict: watch.Stalled, Health: starving},
	}
	run := &scriptedRunner{outcomes: []Outcome{claimed, delivered}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err == nil {
		t.Fatal("the cast reported success though a viewer's playback was ended by a fault")
	}
	if len(run.seen) != 1 {
		t.Fatalf("ran %d attempts, want 1: nothing a watch can say makes a cast someone is watching restartable", len(run.seen))
	}
}

// TestAFailureNobodyJudgedIsAbandonedRatherThanRetried is the second lock under decision 1,
// and the one that holds when the first is wrong.
//
// The outcome here is what a leg that misreports its phase produces: a reader that exited on
// packets it was copying, under an error no watch ever reached, with a phase that says nothing
// was playing. It classifies as a broken copy and the playbook has a recovery for that class,
// so a gate that only read the phase offered it: a fresh work directory, a fresh connect and a
// fresh Play, which for the read whose signed URL expired mid-title is the film restarted from
// the beginning. Abandoning an unjudged failure costs a refusal instead, and a refusal is what
// a phase this layer cannot verify is worth.
func TestAFailureNobodyJudgedIsAbandonedRatherThanRetried(t *testing.T) {
	in := Intent{
		Candidates: []*media.Stream{
			link(t, "https://cdn.example/one.m3u8"),
			link(t, "https://other.example/two.m3u8"),
		},
		Read:     policy(t, media.Origin{Segmented: true, Framing: media.FramingOutOfBand}),
		Deadline: 30 * time.Second,
	}
	// The join a delivery whose input died produces: the encoder noticed the broken stdin, and
	// the supervisor's playing window has no rule for a producer that has already ended, so
	// nothing judged this and the error is the one the reader collected on its way out.
	readErr := errors.New("upstream pull: exit status 1")
	unjudged := Outcome{
		Err: readErr,
		Evidence: Evidence{
			Reached:  PhaseReading,
			ReadErr:  readErr,
			ReadExit: 1,
			Copied:   carriage.Axes{Video: true, Audio: true},
			Health:   watch.Health{Landed: 812 << 20, Speed: 1.9, Headroom: 2, Samples: 4800},
			Lines:    []string{"[https @ 0x] HTTP error 403 Forbidden"},
		},
	}
	run := &scriptedRunner{outcomes: []Outcome{unjudged, delivered}}

	err := Cast(t.Context(), in, run, &fakeProgram{})
	if err == nil {
		t.Fatal("the cast reported success though its reader died")
	}
	if len(run.seen) != 1 {
		t.Fatalf("ran %d attempts, want 1: a failure nobody judged is not one castor knows how to aim at", len(run.seen))
	}
	// The class matters here: the refusal has to be the conjunction's doing and not an empty
	// playbook's, or this case would pass for a reason that has nothing to do with the rule.
	var f *Fault
	if !errors.As(err, &f) {
		t.Fatalf("cast error = %v, want a fault", err)
	}
	if f.Kind != CopyBrokeUpstream {
		t.Fatalf("classified %s, want %s: without a class that HAS a recovery this case proves nothing", f.Kind, CopyBrokeUpstream)
	}
	if _, ok := DecodeAxis.Apply(t.Context(), Change{Intent: in, Attempt: f.Attempt, Outcome: unjudged}); !ok {
		t.Error("the recovery for this class declined on its own, so the refusal was not the gate's doing")
	}
}

// TestCastEndsWithTheCancellationRatherThanAFault is the rule this layer owns for a cast's
// result. Everything castor kills reports a broken pipe on the way out, so a loop that
// classified those would answer Ctrl+C by starting the cast again.
func TestCastEndsWithTheCancellationRatherThanAFault(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	in := Intent{
		Candidates: []*media.Stream{
			link(t, "https://cdn.example/one.m3u8"),
			link(t, "https://other.example/two.m3u8"),
		},
		Deadline: 30 * time.Second,
	}
	// Exactly what the executor folds on a cancelled cast: a stalled-looking read, killed.
	run := &scriptedRunner{outcomes: []Outcome{{
		Err:      context.Canceled,
		Evidence: Evidence{Reached: PhaseReading, Cancelled: true, Verdict: watch.Stalled},
	}, delivered}}

	err := Cast(ctx, in, run, &fakeProgram{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cast error = %v, want the cancellation", err)
	}
	var f *Fault
	if errors.As(err, &f) {
		t.Errorf("a cancelled cast was reported as a %s fault", f.Kind)
	}
	if len(run.seen) != 1 {
		t.Fatalf("ran %d attempts, want 1: a cast the user stopped is not a cast to retry", len(run.seen))
	}
}

// TestTheRefusalNamesWhatWasSpent is what separates "castor could not cast this" from
// "castor tried the things it has, and here is what each of them measured".
func TestTheRefusalNamesWhatWasSpent(t *testing.T) {
	in := Intent{
		Candidates: []*media.Stream{
			link(t, "https://cdn.example/one.m3u8"),
			link(t, "https://other.example/two.m3u8"),
		},
		Deadline: 30 * time.Second,
	}
	// A link that established nothing, which the playbook answers with the next link alone:
	// what this case is about is what a refusal SAYS, so it takes the one class whose
	// recovery is a single step.
	dead := judged(watch.Dead, PhaseReading, watch.Health{})
	run := &scriptedRunner{outcomes: []Outcome{dead, dead}}

	err := Cast(t.Context(), in, run, &fakeProgram{})
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2: the ordering offered a second link", len(run.seen))
	}
	var f *Fault
	if !errors.As(err, &f) {
		t.Fatalf("cast error = %v, want a fault", err)
	}
	if len(f.Tried) != 1 || f.Tried[0] != SwitchCandidate.Name {
		t.Errorf("the refusal spent %v, want exactly [%s]", f.Tried, SwitchCandidate.Name)
	}
	if f.Attempt.Try != 2 || f.Attempt.Candidate != 1 {
		t.Errorf("the refusal describes try %d of candidate %d, want the attempt that actually failed last (2, 1)",
			f.Attempt.Try, f.Attempt.Candidate)
	}
	if !strings.Contains(err.Error(), "already tried: "+SwitchCandidate.Name) {
		t.Errorf("the refusal %q does not say what was already spent", err)
	}
}

// TestTheLedgerStallsALoopAStrategyWouldSpin is the backstop under the strategies' own
// monotonicity. A strategy that hands back an attempt this cast already ran is a table
// edit that broke strict descent, and the honest response is to stop and refuse rather
// than to run the same attempt for as long as the source keeps failing it the same way.
func TestTheLedgerStallsALoopAStrategyWouldSpin(t *testing.T) {
	sameAgain := Strategy{
		Name:  "same-again",
		Why:   "a table edit that lost its strict descent",
		Apply: func(_ context.Context, c Change) (Attempt, bool) { return c.Attempt, true },
	}
	withPlaybook(t, map[Kind][]Strategy{SourceStalled: {sameAgain}})

	in := Intent{Candidates: []*media.Stream{link(t, "https://cdn.example/one.m3u8")}, Deadline: 30 * time.Second}
	stalled := judged(watch.Stalled, PhaseReading, watch.Health{})
	run := &scriptedRunner{outcomes: []Outcome{stalled, stalled, stalled}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err == nil {
		t.Fatal("the cast reported success though every attempt failed")
	}
	if len(run.seen) != 1 {
		t.Fatalf("ran %d attempts, want 1: the second was the first one over again", len(run.seen))
	}
}

// TestTheLedgerSeesEveryChangeAStrategyMakes is the requirement the ledger above puts on
// every strategy, stated from the other end: an attempt whose whole difference the ledger
// cannot see is one it refuses as a repeat, so that recovery is never run at all and the cast
// stops with the fault instead. It is the exact shape the height carried before this: the
// ledger's half of the identity was written without it, so a degrade onto a shorter rung at
// the same bitrate stalled the loop.
//
// Each row changes ONE field a strategy really does change, and asserts that both readers of
// the identity moved: the ledger, or the recovery is dead, and the line, or a reader watching
// two attempts scroll past cannot tell them apart.
func TestTheLedgerSeesEveryChangeAStrategyMakes(t *testing.T) {
	base := Attempt{
		Candidate: 0,
		Source:    link(t, "https://cdn.example/one.m3u8"),
		Rendition: media.Rendition{Bitrate: 5000000, Height: 1080},
		Read:      read.For(read.Shape{Segmented: true, Framing: media.FramingInBand}, 30*time.Second),
		Delivery:  media.DeliveryAuto,
	}

	changed := []struct {
		name string
		by   func(Attempt) Attempt
	}{{
		name: "the next link the ranker admitted (SwitchCandidate)",
		by: func(a Attempt) Attempt {
			a.Candidate, a.Source = 1, link(t, "https://cdn.example/two.m3u8")
			return a
		},
	}, {
		name: "a lighter rung of the same program (DegradeRendition)",
		by: func(a Attempt) Attempt {
			a.Source = link(t, "https://cdn.example/720.m3u8")
			a.Rendition = media.Rendition{Bitrate: 2000000, Height: 720}
			return a
		},
	}, {
		// The rung a source published with no bitrate on it, which is the case the two
		// halves of the identity disagreed about: same link, same bitrate, shorter picture.
		name: "a rung that differs only in height",
		by: func(a Attempt) Attempt {
			a.Rendition.Height = 720
			return a
		},
	}, {
		name: "the same link asked for politely (RelaxRead)",
		by: func(a Attempt) Attempt {
			a.Read, _ = read.Cautious(a.Read)
			return a
		},
	}, {
		name: "a relay instead of a hand-off (ServeInstead)",
		by: func(a Attempt) Attempt {
			a.Delivery = media.DeliveryServe
			return a
		},
	}, {
		name: "an axis whose copy already broke (DecodeBrokenAxis)",
		by: func(a Attempt) Attempt {
			a.Decode.Video = true
			return a
		},
	}}

	for _, tt := range changed {
		t.Run(tt.name, func(t *testing.T) {
			revised := tt.by(base)
			if revised.key() == base.key() {
				t.Errorf("the ledger reads this attempt as one already run (%s), so the strategy that produced it would never run twice", base.key())
			}
			if revised.String() == base.String() {
				t.Errorf("both attempts print as %q, so nothing reading a failed run can tell them apart", base.String())
			}
		})
	}

	// And the field that is NOT part of what an attempt does: a second try of the same
	// attempt is the same attempt, which is what makes the ledger a loop stop rather than a
	// counter of tries.
	again := base
	again.Try = 7
	if again.key() != base.key() {
		t.Error("the try counter is part of the ledger's identity, so a loop that changes nothing else would run forever")
	}
}

// TestAClassWithNothingToOfferIsRefusedWithTheFault pins what a class the playbook answers
// with nothing does: the cast stops on the fault a user has to act on, having run the failed
// attempt exactly once.
//
// A class the playbook was never TOLD about used to be a second error joined onto that
// fault, and it is now caught where it can be acted on instead of on the run that reached
// it: the two coupling tests walk the classification table against the playbook in both
// directions (see TestEveryClassTheTableReachesHasAPlaybookEntry and
// TestEveryVerdictThatEndsACastNamesAClassWithARecovery), so an entry nobody wrote fails the
// suite rather than a viewer's cast.
func TestAClassWithNothingToOfferIsRefusedWithTheFault(t *testing.T) {
	withPlaybook(t, map[Kind][]Strategy{})

	in := Intent{Candidates: []*media.Stream{link(t, "https://cdn.example/one.m3u8")}, Deadline: 30 * time.Second}
	run := &scriptedRunner{outcomes: []Outcome{judged(watch.Stalled, PhaseReading, watch.Health{})}}

	err := Cast(t.Context(), in, run, &fakeProgram{})
	var f *Fault
	if !errors.As(err, &f) || f.Kind != SourceStalled {
		t.Fatalf("cast error = %v, want the fault a user has to act on", err)
	}
	if len(run.seen) != 1 {
		t.Errorf("ran %d attempts though nothing was offered for the class", len(run.seen))
	}
}

// TestCastRefusesAnIntentWithNothingToAttempt covers the one shape the loop must not
// discover halfway through: an index into an empty ordering. Reporting it as a failed
// attempt would send whoever reads the log looking at a source instead of at a caller.
func TestCastRefusesAnIntentWithNothingToAttempt(t *testing.T) {
	run := &scriptedRunner{}
	if err := Cast(t.Context(), Intent{}, run, &fakeProgram{}); err == nil {
		t.Fatal("a cast with no candidate reported success")
	}
	if len(run.seen) != 0 {
		t.Errorf("ran %d attempts over an empty ordering", len(run.seen))
	}
}

// TestCastReturnsWhenTheAttemptWorked is the ordinary path: one attempt, no fault, nothing
// classified and nothing revised.
func TestCastReturnsWhenTheAttemptWorked(t *testing.T) {
	in := Intent{Candidates: []*media.Stream{link(t, "https://cdn.example/one.m3u8")}, Deadline: 30 * time.Second}
	run := &scriptedRunner{outcomes: []Outcome{delivered}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 1 {
		t.Fatalf("ran %d attempts, want 1", len(run.seen))
	}
}

// TestTheFirstAttemptIsTheIntentsHeadLinkOnItsOwnTerms pins what a cast starts from, since
// every strategy is a change to it and a wrong starting point is a wrong cast even when
// nothing is revised.
func TestTheFirstAttemptIsTheIntentsHeadLinkOnItsOwnTerms(t *testing.T) {
	origin := ladder(t, rung(t, "https://cdn.example/1080.m3u8", 3000000, 1080))
	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/master.m3u8"), link(t, "https://other.example/two.m3u8")},
		Origin:     origin,
		Read:       policy(t, origin),
		Deadline:   30 * time.Second,
	}
	run := &scriptedRunner{outcomes: []Outcome{delivered}}
	if err := Cast(t.Context(), in, run, &fakeProgram{}); err != nil {
		t.Fatalf("cast: %v", err)
	}

	first := run.seen[0]
	if first.Try != 1 || first.Candidate != 0 || first.Source != in.Candidates[0] {
		t.Errorf("first attempt = try %d of candidate %d (%s), want try 1 of candidate 0 (%s)",
			first.Try, first.Candidate, first.Source.URL, in.Candidates[0].URL)
	}
	if first.Read.Name != in.Read.Name || first.Origin.Duration != origin.Duration {
		t.Errorf("first attempt reads %q over %+v, want the intent's own policy %q and origin",
			first.Read.Name, first.Origin, in.Read.Name)
	}
}

// TestDegradeRenditionReportsInapplicableRatherThanInventingARung states the three ways
// this recovery declines, each of which is the absence of evidence and not an answer to it.
// A recovery that answered anyway would move a cast onto a rung nobody measured, which on a
// ladder whose declared rates are missing is how a degrade lands heavier than the rung it
// was escaping.
func TestDegradeRenditionReportsInapplicableRatherThanInventingARung(t *testing.T) {
	top := rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160)
	low := rung(t, "https://cdn.example/720.m3u8", 1000000, 720)

	cases := []struct {
		name string
		why  string
		a    Attempt
		out  Outcome
	}{{
		name: "a source that published one rendition",
		why:  "there was nothing to choose from, so there is no rung to fall back to (media.Origin.Sole)",
		a:    Attempt{Source: link(t, "https://cdn.example/2160.m3u8"), Origin: ladder(t, top), Rendition: top},
		out:  judged(watch.Undeliverable, PhaseReading, starving),
	}, {
		name: "a rung whose rate the source never declared",
		why:  "an undeclared rate offers no ceiling to measure a lighter rung against",
		a:    Attempt{Source: link(t, "https://cdn.example/2160.m3u8"), Origin: ladder(t, top, low)},
		out:  judged(watch.Undeliverable, PhaseReading, starving),
	}, {
		name: "a read that fell short of playback while still running ahead of realtime",
		why:  "its measured ceiling sits ABOVE its own rung, and the move has to be down the ladder or not at all",
		a:    Attempt{Source: link(t, "https://cdn.example/720.m3u8"), Origin: ladder(t, top, low), Rendition: low},
		out: judged(watch.Undeliverable, PhaseReading,
			watch.Health{Landed: 4 << 20, Speed: 1.4, Headroom: 2, Samples: 4}),
	}, {
		name: "a read that never stated a speed",
		why:  "nothing was measured, so the ceiling would be a guess about a link castor has no figure for",
		a:    Attempt{Source: link(t, "https://cdn.example/2160.m3u8"), Origin: ladder(t, top, low), Rendition: top},
		out:  judged(watch.Undeliverable, PhaseReading, watch.Health{Landed: 33088, Headroom: 2}),
	}}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := DegradeRendition.Apply(t.Context(), Change{Attempt: tt.a, Outcome: tt.out}); ok {
				t.Errorf("offered a degrade: %s", tt.why)
			}
		})
	}
}

// broke is a read that exited on packets it was passing through BEFORE the playback gate
// opened: the observed shape, where a truncated fMP4 fragment desynchronised the bitstream
// filter a copy into the buffer's container carries and the reader exited 183.
//
// The gate is where that death is noticed, so the attempt's error is the gate's own fault over
// the reader's error, which is what the executor folds. The same exit past the gate is a
// different outcome entirely and never a revisable one (see
// TestAFailureNobodyJudgedIsAbandonedRatherThanRetried).
func broke(copied carriage.Axes, lines ...string) Outcome {
	dead := errors.New("upstream pull: exit status 183")
	health := watch.Health{Landed: 4 << 20, Speed: 1.8, Headroom: 2, Samples: 12}
	return Outcome{
		Err: &watch.Fault{
			Kind:     watch.Dead,
			Why:      "the source read reached a terminal error before playback could start",
			Subject:  "playback gate",
			Window:   watch.BeforePlay,
			Revise:   true,
			Health:   health,
			Err:      dead,
			Evidence: lines,
		},
		Evidence: Evidence{
			Reached:  PhaseReading,
			Verdict:  watch.Dead,
			Health:   health,
			ReadErr:  dead,
			ReadExit: 183,
			Copied:   copied,
			Lines:    lines,
		},
	}
}

// TestCastDecodesTheAxisWhoseCopyBrokeUpstream is the recovery for the failure decision 1
// gave up on resuming. Prevention owns its trigger, and when it happens anyway the answer
// is to stop passing those packets through: decoded, a truncated fragment costs a re-encode
// and produces packets nothing has to resynchronise.
func TestCastDecodesTheAxisWhoseCopyBrokeUpstream(t *testing.T) {
	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/one.m3u8")},
		Read:       policy(t, media.Origin{Segmented: true, Framing: media.FramingOutOfBand}),
		Deadline:   30 * time.Second,
	}
	run := &scriptedRunner{outcomes: []Outcome{
		broke(carriage.Axes{Video: true, Audio: true}, "[hls @ 0x] Invalid NAL unit size (-1140850681 > 97253)"),
		delivered,
	}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2", len(run.seen))
	}
	second := run.seen[1]
	if !second.Decode.Video || !second.Decode.Audio {
		t.Errorf("second attempt decodes %s, want every axis the reader was copying: which half broke is not knowable from an exit status", second.Decode)
	}
	if second.Source != in.Candidates[0] || second.Try != 2 {
		t.Errorf("second attempt = try %d of %s, want try 2 of the same link: the copy is what failed, not the link",
			second.Try, second.Source.URL)
	}
	if run.seen[0].Decode.Any() {
		t.Error("the first attempt was told to decode something, so a cast now re-encodes before it has any evidence to")
	}
}

// TestACopyThatBrokeIsNotBlamedTwice pins the strict descent that makes this terminate: the
// set of copied axes only ever shrinks, so once there is nothing left to stop copying the
// recovery declines and the cast is refused rather than re-encoding the same title again.
func TestACopyThatBrokeIsNotBlamedTwice(t *testing.T) {
	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/one.m3u8")},
		Read:       policy(t, media.Origin{Segmented: true}),
		Deadline:   30 * time.Second,
	}
	// The second reader copied nothing (both axes were being decoded) and still died, so
	// there is no copy left to blame.
	run := &scriptedRunner{outcomes: []Outcome{
		broke(carriage.Axes{Video: true, Audio: true}),
		broke(carriage.Axes{}),
		delivered,
	}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err == nil {
		t.Fatal("the cast reported success though every attempt failed")
	}
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2: there is no third thing to stop copying", len(run.seen))
	}
	// The strategy's own descent, stated directly rather than left to the ledger. The ledger
	// would absorb the repeat either way, and a recovery that relies on it is a recovery whose
	// bound nobody wrote down.
	spent := Change{
		Attempt: Attempt{Decode: carriage.Axes{Video: true, Audio: true}},
		Outcome: broke(carriage.Axes{Video: true}),
	}
	if _, ok := DecodeAxis.Apply(t.Context(), spent); ok {
		t.Error("offered to stop copying an axis this attempt was already decoding")
	}
}

// TestCastRelaxesAStalledReadBeforeAbandoningTheLink is the one thing about HOW a link is
// read that a stall gives any reason to change, and it is castor's own doing: every VOD read
// opens by demanding ninety seconds of stream as fast as the wire will carry it, and a burst
// of requests behind one signature against these hosts is the documented way to earn a rate
// limiter's silence.
func TestCastRelaxesAStalledReadBeforeAbandoningTheLink(t *testing.T) {
	origin := media.Origin{Segmented: true}
	in := Intent{
		Candidates: []*media.Stream{
			link(t, "https://cdn.example/one.m3u8"),
			link(t, "https://other.example/two.m3u8"),
		},
		Origin:   origin,
		Read:     policy(t, origin),
		Deadline: 30 * time.Second,
	}
	stalled := judged(watch.Stalled, PhaseReading, watch.Health{Landed: 33088})
	run := &scriptedRunner{outcomes: []Outcome{stalled, stalled, delivered}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 3 {
		t.Fatalf("ran %d attempts, want 3: the same link read politely, then the next link", len(run.seen))
	}

	second := run.seen[1]
	if second.Source != in.Candidates[0] {
		t.Errorf("the second attempt changed link (%s) before it had asked the first one politely", second.Source.URL)
	}
	if second.Read.Pace.Realtime != 1 || second.Read.Pace.Burst != 0 {
		t.Errorf("the relaxed read is paced %.2gx with a %s burst, want playback pace and no burst",
			second.Read.Pace.Realtime, second.Read.Pace.Burst)
	}
	if second.Read.Deadline != in.Read.Deadline || second.Read.Backoff != in.Read.Backoff {
		t.Error("relaxing the read changed the deadline or the backoff, which the source's shape called for and a stall is no evidence against")
	}
	// And then the link, because a policy that has nothing left to give up declines: the
	// third attempt is the ordering being walked and not the pace being lowered twice.
	if third := run.seen[2]; third.Candidate != 1 {
		t.Errorf("third attempt reads candidate %d, want the next link the ranker admitted", third.Candidate)
	}
}

// TestARelaxedReadIsNotRelaxedAgain is the strategy's own bound: it moves the pace down one
// step to a value with nothing below it, so it answers false the second time it is asked and
// the loop cannot spend a viewer's time asking the same link the same way.
func TestARelaxedReadIsNotRelaxedAgain(t *testing.T) {
	cautious, ok := read.Cautious(policy(t, media.Origin{Segmented: true}))
	if !ok {
		t.Fatal("a VOD read has a burst to give up")
	}
	if _, ok := RelaxRead.Apply(t.Context(), Change{Attempt: Attempt{Read: cautious}}); ok {
		t.Error("offered to relax a read that is already at playback pace with no burst")
	}
}

// TestServeInsteadStopsHandingTheRendererAURL is the recovery for a source castor cannot
// convict: one that lies about itself and so looks fetchable while the renderer refuses it.
// It needs no new evidence, only the value the operator's one knob already writes.
func TestServeInsteadStopsHandingTheRendererAURL(t *testing.T) {
	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/one.mp4")},
		Deadline:   30 * time.Second,
	}
	refused := Outcome{
		Err:      errors.New("starting playback: SOAP SetAVTransportURI: 714"),
		Evidence: Evidence{PlayErr: errors.New("SOAP SetAVTransportURI: 714")},
	}
	run := &scriptedRunner{outcomes: []Outcome{refused, delivered}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2", len(run.seen))
	}
	if got := run.seen[1].Delivery; got != media.DeliveryServe {
		t.Errorf("second attempt delivers %q, want %q: the renderer would not fetch the source itself", got, media.DeliveryServe)
	}
	if _, ok := ServeInstead.Apply(t.Context(), Change{Attempt: run.seen[1]}); ok {
		t.Error("offered to serve a cast that was already being served, which is a refusal it cannot answer")
	}
}

// TestTheRefusalCarriesTheArithmetic is the whole point of the terminal message. The run this
// layer was built for ended as a TV error and bytes_sent=0 while castor held every term of
// the sentence a user needed.
func TestTheRefusalCarriesTheArithmetic(t *testing.T) {
	// The observed source: one rendition, 2h1m55s of program, delivering a sixteenth of
	// realtime against a reader allowed twice it.
	sole := media.Origin{
		Renditions: []media.Rendition{rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160)},
		Segmented:  true,
		Framing:    media.FramingOutOfBand,
		Duration:   2*time.Hour + time.Minute + 55*time.Second,
	}
	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/2160.m3u8")},
		Origin:     sole,
		Rendition:  sole.Renditions[0],
		Read:       policy(t, sole),
		Deadline:   30 * time.Second,
	}
	health := watch.Health{Landed: 33088, Position: time.Second, Speed: 0.0627, Headroom: 2, Samples: 4}
	run := &scriptedRunner{outcomes: []Outcome{judged(watch.Undeliverable, PhaseReading, health)}}

	err := Cast(t.Context(), in, run, &fakeProgram{})
	if err == nil {
		t.Fatal("the cast reported success though its only attempt failed")
	}
	msg := err.Error()
	for _, want := range []struct{ text, why string }{
		{"speed=0.0627", "the measurement the refusal was reached on"},
		{"2h1m55s", "the runtime the source published, which is the numerator of every honest statement about how long this takes"},
		{"32h24m", "the projected runtime at the measured speed, which is what turns a slow cast into a number"},
		{"the source published one rendition", "whether there was anything lighter to fall back to"},
		{"candidate 1 of 1", "how much of the ordering was spent"},
		{"every one of the 1 links", "that castor did not stop early"},
	} {
		if !strings.Contains(msg, want.text) {
			t.Errorf("the refusal %q does not carry %q: %s", msg, want.text, want.why)
		}
	}
}

// TestTheRefusalQuotesTheReaderWithoutBeingDecidedByIt keeps prose in its place: the line
// that names a truncated bitstream is what makes an opaque exit status actionable, and it
// decides nothing. The class and the recovery are reached before it is read.
func TestTheRefusalQuotesTheReaderWithoutBeingDecidedByIt(t *testing.T) {
	const tell = "[hls @ 0x] Invalid NAL unit size (-1140850681 > 97253)"
	in := Intent{
		Candidates: []*media.Stream{link(t, "https://cdn.example/one.m3u8")},
		Read:       policy(t, media.Origin{Segmented: true, Framing: media.FramingOutOfBand}),
		Deadline:   30 * time.Second,
	}
	// Both axes already decoded, so nothing is left to stop copying and the cast is refused
	// on the reader's own account.
	run := &scriptedRunner{outcomes: []Outcome{broke(carriage.Axes{}, "Opening segment 41", tell)}}

	err := Cast(t.Context(), in, run, &fakeProgram{})
	var f *Fault
	if !errors.As(err, &f) {
		t.Fatalf("cast error = %v, want a fault", err)
	}
	if !strings.Contains(err.Error(), tell) {
		t.Errorf("the refusal %q does not quote the line that names what broke", err)
	}
	if !strings.Contains(err.Error(), "exit status 183") {
		t.Errorf("the refusal %q does not carry the reader's own exit status", err)
	}
}

// withPlaybook swaps the shipped table for one case, so a property of the loop (the ledger
// stalling a repeat, a missing entry being reported) is exercisable without a strategy in
// the shipped table having to be broken to reach it.
func withPlaybook(t *testing.T, table map[Kind][]Strategy) {
	t.Helper()
	shipped := playbook
	playbook = table
	t.Cleanup(func() { playbook = shipped })
}
