package attempt

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/compose"
	"github.com/stupside/castor/internal/cast/health"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// scriptedRunner answers attempts in sequence and records what it was asked to run.
type scriptedRunner struct {
	outcomes []Outcome
	seen     []Attempt
}

func (r *scriptedRunner) Run(_ context.Context, a Attempt) Outcome {
	r.seen = append(r.seen, a)
	if len(r.seen) > len(r.outcomes) {
		return Outcome{Err: errors.New("the loop ran an attempt nothing was scripted for")}
	}
	return r.outcomes[len(r.seen)-1]
}

// fakeProgram publishes scripted answers per link and every other link unchanged.
type fakeProgram struct {
	answers  map[string]published
	asked    []string
	narrowed []source.Rendition
}

type published struct {
	url    string
	origin source.Origin
	rung   source.Rendition
	err    error
}

func (p *fakeProgram) RefetchProgram(_ context.Context, s *source.Stream, chosen source.Rendition) (source.Resolution, error) {
	p.asked = append(p.asked, s.URL.String())
	if !reflect.ValueOf(chosen).IsZero() {
		p.narrowed = append(p.narrowed, chosen)
	}
	answer, ok := p.answers[s.URL.String()]
	switch {
	case !ok:
		program, err := programForStream(s, chosen.AudioURL)
		return source.Resolution{Program: program}, err
	case answer.err != nil:
		return source.Resolution{}, answer.err
	}
	link := *s
	if answer.url != "" {
		u, err := url.Parse(answer.url)
		if err != nil {
			panic(err)
		}
		link.URL = u
	}
	program, err := programForStream(&link, chosen.AudioURL)
	if err == nil {
		program.Inputs[0].Fetch = media.Fetch{Segmented: answer.origin.Segmented, Framing: answer.origin.Framing, Live: answer.origin.Live}
	}
	return source.Resolution{Program: program, Origin: answer.origin, Rendition: answer.rung}, err
}

func publishing(head *source.Stream, origin source.Origin, chosen source.Rendition) *fakeProgram {
	return &fakeProgram{answers: map[string]published{head.URL.String(): {origin: origin, rung: chosen}}}
}

// judged is an outcome the watch ended, revisable only before playback.
func judged(k health.Kind, reached Phase, h health.Health) Outcome {
	return Outcome{
		Err:      &health.Fault{Kind: k, Subject: "the cast under watch", Revise: reached < PhasePlaying, Health: h},
		Evidence: Evidence{Reached: reached, Verdict: k, Health: h},
	}
}

var delivered = Outcome{Evidence: Evidence{Reached: PhaseDelivered}}

// starving is an observed run: 33KB and 1s of media in 30s.
var starving = health.Health{Landed: 33088, Position: time.Second, Speed: 0.159, Headroom: 2, Samples: 4}

func link(t *testing.T, raw string) *source.Stream {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &source.Stream{URL: u, ContentType: media.HLS, Headers: http.Header{"Referer": {"https://player.example/"}}}
}

func programForStream(stream *source.Stream, audio *url.URL) (media.Program, error) {
	if audio == nil {
		return source.ProgramFor(stream)
	}
	return media.NewProgram(media.Program{Inputs: []media.Input{
		{ID: media.PrimaryInputID, URL: stream.URL, Headers: stream.Headers, ContentType: stream.ContentType},
		{ID: media.AudioInputID, URL: audio, Headers: stream.Headers, ContentType: stream.ContentType},
	}, Tracks: []media.TrackRef{
		{Input: media.PrimaryInputID, Kind: media.TrackVideo},
		{Input: media.AudioInputID, Kind: media.TrackAudio},
	}, ClockInput: media.PrimaryInputID, EndPolicy: media.EndAtShortest})
}

func primaryInput(t *testing.T, program media.Program) media.Input {
	t.Helper()
	input, ok := program.PrimaryInput()
	if !ok {
		t.Fatalf("program has no primary input: %+v", program)
	}
	return input
}

func ladder(rungs ...source.Rendition) source.Origin {
	return source.Origin{Renditions: rungs, Segmented: true, Duration: 2 * time.Hour}
}

func rung(t *testing.T, raw string, bitrate media.Bitrate, height int) source.Rendition {
	t.Helper()
	return source.Rendition{URL: link(t, raw).URL, Bitrate: bitrate, Height: height}
}

func candidates(t *testing.T, raws ...string) []*source.Stream {
	t.Helper()
	var out []*source.Stream
	for _, raw := range raws {
		out = append(out, link(t, raw))
	}
	return out
}

func mustFault(t *testing.T, err error) *fault {
	t.Helper()
	f, ok := errors.AsType[*fault](err)
	if !ok {
		t.Fatalf("cast error = %v, want a fault", err)
	}
	return f
}

func TestOnlyARendererHandedTheSourceIsServedInstead(t *testing.T) {
	refusal := errors.New("SOAP 714")
	for _, tc := range []struct {
		name    string
		handoff bool
		want    string
	}{
		{"handed the source", true, serveInstead.name},
		{"already served", false, switchCandidate.name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := Intent{Candidates: candidates(t, "https://cdn.example/one.m3u8", "https://other.example/two.m3u8"), Deadline: 30 * time.Second}
			refused := Outcome{Err: refusal, Evidence: Evidence{Reached: PhaseOpening, PlayErr: refusal, Handoff: tc.handoff}}
			run := &scriptedRunner{outcomes: []Outcome{refused, delivered}}
			if err := Cast(t.Context(), in, run, &fakeProgram{}); err != nil || len(run.seen) != 2 {
				t.Fatalf("Cast error = %v after %d attempts, want the second attempt to deliver", err, len(run.seen))
			}
			got := switchCandidate.name
			if next := run.seen[1]; next.candidate == 0 && next.Delivery == compose.DeliveryServe {
				got = serveInstead.name
			}
			if got != tc.want {
				t.Errorf("the refusal was answered by %s, want %s", got, tc.want)
			}
		})
	}
}

func TestUnreadableLinksAreMovedPastInRankOrder(t *testing.T) {
	in := Intent{Candidates: candidates(t,
		"https://cdn.example/expired.m3u8",
		"https://other.example/two.m3u8",
		"https://third.example/403.m3u8",
		"https://fourth.example/four.m3u8",
	), Deadline: 30 * time.Second}
	prog := &fakeProgram{answers: map[string]published{
		"https://cdn.example/expired.m3u8": {err: errors.New("HTTP 403")},
		"https://third.example/403.m3u8":   {err: errors.New("HTTP 403")},
	}}
	run := &scriptedRunner{outcomes: []Outcome{judged(health.Dead, PhaseReading, health.Health{}), delivered}}

	if err := Cast(t.Context(), in, run, prog); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 || run.seen[0].candidate != 1 || run.seen[1].candidate != 3 {
		t.Fatalf("attempts = %+v, want candidates 1 then 3", run.seen)
	}
}

func TestATimelineCastorCouldNotReadMovesToTheNextLink(t *testing.T) {
	in := Intent{Candidates: candidates(t, "https://cdn.example/one.mpd", "https://other.example/two.m3u8"), Deadline: 30 * time.Second}
	unread := errors.New("HTTP 503")
	failed := Outcome{Err: unread, Evidence: Evidence{Reached: PhaseReading, TimelineErr: unread}}
	run := &scriptedRunner{outcomes: []Outcome{failed, delivered}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 || run.seen[1].candidate != 1 {
		t.Fatalf("attempts = %+v, want the second link after the first's timeline could not be read", run.seen)
	}
}

func TestACastWithNoReadableLinkRunsNothing(t *testing.T) {
	in := Intent{Candidates: candidates(t, "https://cdn.example/one.m3u8"), Deadline: 30 * time.Second}
	prog := &fakeProgram{answers: map[string]published{"https://cdn.example/one.m3u8": {err: errors.New("HTTP 410")}}}
	run := &scriptedRunner{}

	if err := Cast(t.Context(), in, run, prog); err == nil || len(run.seen) != 0 {
		t.Fatalf("Cast error = %v, attempts = %d; want a refusal before anything ran", err, len(run.seen))
	}
}

func TestCastSwitchesToTheNextLinkOnItsOwnLadder(t *testing.T) {
	sole := source.Origin{Renditions: []source.Rendition{rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160)}, Segmented: true}
	other := ladder(rung(t, "https://other.example/1080.m3u8", 3000000, 1080), rung(t, "https://other.example/720.m3u8", 1200000, 720))
	other.Framing = media.FramingInBand
	in := Intent{Candidates: candidates(t, "https://cdn.example/2160.m3u8", "https://other.example/master.m3u8"), Deadline: 30 * time.Second}
	prog := &fakeProgram{answers: map[string]published{
		"https://cdn.example/2160.m3u8":     {origin: sole, rung: sole.Renditions[0]},
		"https://other.example/master.m3u8": {url: "https://other.example/720.m3u8", origin: other, rung: other.Renditions[1]},
	}}
	run := &scriptedRunner{outcomes: []Outcome{judged(health.Undeliverable, PhaseReading, starving), delivered}}

	if err := Cast(t.Context(), in, run, prog); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2", len(run.seen))
	}
	second := run.seen[1]
	if second.candidate != 1 || primaryInput(t, second.Program).URL.String() != "https://other.example/720.m3u8" {
		t.Errorf("second attempt = candidate %d reading %s, want candidate 1 on its narrowed rung", second.candidate, primaryInput(t, second.Program).URL)
	}
	if second.Rendition.Bitrate != 1200000 || len(second.Origin.Renditions) != 2 {
		t.Errorf("second attempt carries rung %d over %d renditions, want the new link's own ladder", second.Rendition.Bitrate, len(second.Origin.Renditions))
	}
	if got := second.Fetch.Primary(second.Program).Name; got != "segment-in-band" {
		t.Errorf("second attempt reads on %q, want the policy of the new link's own framing", got)
	}
}

func TestCastDegradesToTheHeaviestRungTheLinkCarried(t *testing.T) {
	top := rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160)
	mid := rung(t, "https://cdn.example/1080.m3u8", 3000000, 1080)
	low := rung(t, "https://cdn.example/720.m3u8", 1000000, 720)
	low.AudioURL = link(t, "https://cdn.example/audio/low.m3u8").URL
	head := link(t, "https://cdn.example/2160.m3u8")
	head.Probe = &media.ProbeInfo{VideoHeight: 2160}
	in := Intent{Candidates: []*source.Stream{head}, Deadline: 30 * time.Second}
	run := &scriptedRunner{outcomes: []Outcome{judged(health.Undeliverable, PhaseReading, starving), delivered}}
	resolver := publishing(head, ladder(top, mid, low), top)

	if err := Cast(t.Context(), in, run, resolver); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2", len(run.seen))
	}
	// 0.159 x 6.9 Mbit/s carries about 1.1 Mbit/s, so 720p is the heaviest rung that fits.
	if !reflect.DeepEqual(resolver.narrowed, []source.Rendition{low}) {
		t.Errorf("narrowed to %+v, want %+v", resolver.narrowed, low)
	}
	second := run.seen[1]
	primary := primaryInput(t, second.Program)
	if primary.URL.String() != low.URL.String() || primary.Headers.Get("Referer") == "" {
		t.Errorf("second attempt reads %s with headers %v, want the 720p rung under the link's headers", primary.URL, primary.Headers)
	}
	if _, measured := second.Program.Measurement(); measured {
		t.Error("the degraded attempt kept probe facts measured on the rung that failed")
	}
}

func TestDegradeOnlyMovesDownTheLadder(t *testing.T) {
	top := rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160)
	low := rung(t, "https://cdn.example/720.m3u8", 1000000, 720)
	program, err := source.ProgramFor(link(t, "https://cdn.example/720.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	a := Attempt{Program: program, Origin: ladder(top, low), Rendition: low}
	ahead := judged(health.Undeliverable, PhaseReading, health.Health{Landed: 4 << 20, Speed: 1.4, Headroom: 2, Samples: 4})
	if _, ok := degradeRendition.apply(t.Context(), change{attempt: a, outcome: ahead}); ok {
		t.Error("a read measured above its own rung was offered a heavier one")
	}
}

func TestCastRelaxesAStalledReadBeforeAbandoningTheLink(t *testing.T) {
	in := Intent{Candidates: candidates(t, "https://cdn.example/one.m3u8", "https://other.example/two.m3u8"), Deadline: 30 * time.Second}
	stalled := judged(health.Stalled, PhaseReading, health.Health{Landed: 33088})
	run := &scriptedRunner{outcomes: []Outcome{stalled, stalled, delivered}}

	if err := Cast(t.Context(), in, run, publishing(in.Candidates[0], source.Origin{Segmented: true}, source.Rendition{})); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 3 {
		t.Fatalf("ran %d attempts, want 3: the same link politely, then the next", len(run.seen))
	}
	second := run.seen[1]
	if second.candidate != 0 {
		t.Errorf("the second attempt changed link before asking the first one politely")
	}
	if pace := second.Fetch.Primary(second.Program).Pace; pace.Realtime != 1 || pace.Burst != 0 {
		t.Errorf("the relaxed read is paced %+v, want playback pace with no burst", pace)
	}
	if run.seen[2].candidate != 1 {
		t.Errorf("third attempt reads candidate %d, want the next link", run.seen[2].candidate)
	}
}

func TestCastDecodesTheAxisWhoseCopyBrokeUpstream(t *testing.T) {
	in := Intent{Candidates: candidates(t, "https://cdn.example/one.m3u8"), Deadline: 30 * time.Second}
	run := &scriptedRunner{outcomes: []Outcome{broke(media.Axes{Video: true, Audio: true}), delivered}}

	if err := Cast(t.Context(), in, run, publishing(in.Candidates[0], source.Origin{Segmented: true}, source.Rendition{})); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if len(run.seen) != 2 {
		t.Fatalf("ran %d attempts, want 2", len(run.seen))
	}
	if run.seen[0].Decode.Any() {
		t.Error("the first attempt decoded before any evidence asked it to")
	}
	if second := run.seen[1]; !second.Decode.Video || !second.Decode.Audio || second.candidate != 0 {
		t.Errorf("second attempt decodes %s on candidate %d, want every copied axis on the same link", second.Decode, second.candidate)
	}
}

func TestAPlayingCastIsNotRevisedEvenByAFaultThatClaimsItCanBe(t *testing.T) {
	in := Intent{Candidates: candidates(t, "https://cdn.example/one.m3u8", "https://other.example/two.m3u8"), Deadline: 30 * time.Second}
	claimed := Outcome{
		Err:      &health.Fault{Kind: health.Stalled, Revise: true, Health: starving},
		Evidence: Evidence{Reached: PhasePlaying, Verdict: health.Stalled, Health: starving},
	}
	run := &scriptedRunner{outcomes: []Outcome{claimed, delivered}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err == nil || len(run.seen) != 1 {
		t.Fatalf("Cast error = %v after %d attempts, want a refusal after 1", err, len(run.seen))
	}
}

func TestAFailureNobodyJudgedIsAbandonedRatherThanRetried(t *testing.T) {
	in := Intent{Candidates: candidates(t, "https://cdn.example/one.m3u8", "https://other.example/two.m3u8"), Deadline: 30 * time.Second}
	readErr := errors.New("upstream pull: exit status 1")
	// Classifies as copy-broke-upstream, whose recovery would apply, so only the gate refuses it.
	unjudged := Outcome{Err: readErr, Evidence: Evidence{Reached: PhaseReading, ReadErr: readErr, ReadExit: 1, Copied: media.Axes{Video: true}}}
	run := &scriptedRunner{outcomes: []Outcome{unjudged, delivered}}

	err := Cast(t.Context(), in, run, publishing(in.Candidates[0], source.Origin{Segmented: true}, source.Rendition{}))
	if f := mustFault(t, err); f.kind != copyBrokeUpstream || len(run.seen) != 1 {
		t.Fatalf("classified %s after %d attempts, want %s after 1", f.kind, len(run.seen), copyBrokeUpstream)
	}
}

func TestARendererThatIsGoneIsNotRetried(t *testing.T) {
	head := link(t, "https://cdn.example/2160.m3u8")
	in := Intent{Candidates: []*source.Stream{head, link(t, "https://cdn.example/two.m3u8")}, Deadline: 30 * time.Second}
	origin := ladder(rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160), rung(t, "https://cdn.example/720.m3u8", 1000000, 720))
	gone := &media.Gone{Renderer: "Living Room TV", Err: errors.New("connect: no route to host")}
	// Unstarted with a play error is revisable, so only the empty playbook entry refuses it.
	away := Outcome{Err: gone, Evidence: Evidence{Reached: phaseUnstarted, PlayErr: gone, RendererGone: gone, Health: starving}}
	run := &scriptedRunner{outcomes: []Outcome{away, delivered}}

	err := Cast(t.Context(), in, run, publishing(head, origin, origin.Renditions[0]))
	f := mustFault(t, err)
	if f.kind != rendererGone || len(run.seen) != 1 || len(f.tried) != 0 {
		t.Fatalf("classified %s after %d attempts having tried %v, want %s after 1 with nothing tried", f.kind, len(run.seen), f.tried, rendererGone)
	}
	if !errors.Is(err, gone.Err) {
		t.Error("the refusal does not unwrap to the renderer's own failure")
	}
}

func TestTheLedgerStopsAStrategyRepeatingAnAttempt(t *testing.T) {
	sameAgain := strategy{name: "same-again", apply: func(_ context.Context, c change) (Attempt, bool) { return c.attempt, true }}
	shipped := playbook
	playbook = map[kind][]strategy{sourceStalled: {sameAgain}}
	t.Cleanup(func() { playbook = shipped })

	in := Intent{Candidates: candidates(t, "https://cdn.example/one.m3u8"), Deadline: 30 * time.Second}
	stalled := judged(health.Stalled, PhaseReading, health.Health{})
	run := &scriptedRunner{outcomes: []Outcome{stalled, stalled, stalled}}

	if err := Cast(t.Context(), in, run, &fakeProgram{}); err == nil || len(run.seen) != 1 {
		t.Fatalf("Cast error = %v after %d attempts, want a refusal after 1", err, len(run.seen))
	}
}

func TestCastEndsWithTheCancellationRatherThanAFault(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	in := Intent{Candidates: candidates(t, "https://cdn.example/one.m3u8", "https://other.example/two.m3u8"), Deadline: 30 * time.Second}
	run := &scriptedRunner{outcomes: []Outcome{{
		Err:      context.Canceled,
		Evidence: Evidence{Reached: PhaseReading, Cancelled: true, Verdict: health.Stalled},
	}, delivered}}

	err := Cast(ctx, in, run, &fakeProgram{})
	if _, isFault := errors.AsType[*fault](err); !errors.Is(err, context.Canceled) || isFault || len(run.seen) != 1 {
		t.Fatalf("Cast error = %v after %d attempts, want the bare cancellation after 1", err, len(run.seen))
	}
}

func TestTheRefusalNamesWhatWasTriedAndItsMeasurements(t *testing.T) {
	sole := source.Origin{
		Renditions: []source.Rendition{rung(t, "https://cdn.example/2160.m3u8", 6941000, 2160)},
		Segmented:  true,
		Duration:   2*time.Hour + time.Minute + 55*time.Second,
	}
	in := Intent{Candidates: candidates(t, "https://cdn.example/2160.m3u8", "https://other.example/two.m3u8"), Deadline: 30 * time.Second}
	slow := health.Health{Landed: 33088, Position: time.Second, Speed: 0.0627, Headroom: 2, Samples: 4}
	run := &scriptedRunner{outcomes: []Outcome{judged(health.Undeliverable, PhaseReading, slow), judged(health.Undeliverable, PhaseReading, slow)}}
	prog := &fakeProgram{answers: map[string]published{
		"https://cdn.example/2160.m3u8":  {origin: sole, rung: sole.Renditions[0]},
		"https://other.example/two.m3u8": {origin: sole, rung: sole.Renditions[0]},
	}}

	err := Cast(t.Context(), in, run, prog)
	f := mustFault(t, err)
	if f.kind != underDelivering || !slices.Equal(f.tried, []string{switchCandidate.name}) || f.attempt.candidate != 1 {
		t.Errorf("refused %s on candidate %d having tried %v, want %s on candidate 1 having tried [%s]",
			f.kind, f.attempt.candidate, f.tried, underDelivering, switchCandidate.name)
	}
	for _, want := range []string{"speed=0.0627", "2h1m55s", "32h24m", "candidate 2 of 2", "already tried: " + switchCandidate.name} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not carry %q", err, want)
		}
	}
}

func broke(copied media.Axes) Outcome {
	dead := errors.New("upstream pull: exit status 183")
	measured := health.Health{Landed: 4 << 20, Speed: 1.8, Headroom: 2, Samples: 12}
	return Outcome{
		Err:      &health.Fault{Kind: health.Dead, Revise: true, Health: measured, Err: dead},
		Evidence: Evidence{Reached: PhaseReading, Verdict: health.Dead, Health: measured, ReadErr: dead, ReadExit: 183, Copied: copied},
	}
}
