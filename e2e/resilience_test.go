package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/cast/policy/watch"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// Package e2e tests castor against hostile origins to verify refusal logic.

// hostileCase is one origin pathology and the answer castor must reach about it.
type hostileCase struct {
	name  string
	how   hostility
	shape hostileShape

	// origin states whether mid-read deadline applies; the axis two cases exist for.
	origin source.Origin

	// verdict is what castor must conclude; a test rule != actual shipping wiring.
	verdict watch.Kind

	// landed: whether media reached buffer before verdict (rate vs. silence).
	landed bool

	// starved requires measured deficit (speed under playback rate) to verify verdict.
	starved bool

	// silence requires a producer that delivered nothing for the derived stall window.
	silence bool

	// readFailed requires terminal failure; exit status > 0 is reader's, < 0 is killed.
	readFailed bool

	// tells: substrings stderr must carry (reader killed, error path never runs).
	tells []string

	// within: wall-clock bound and case context (missed = "never ruled" not fail).
	within time.Duration
}

// probeBudget is what cast.Measure gives the source probe; not configurable or skippable.
const probeBudget = read.BackoffMax / 2

// schedulingSlack allows the machine to be slow; bounds must prove castor ruled at all.
const schedulingSlack = 120 * time.Second

// verdictMargin covers watch polling and stall detection, nothing else.
const verdictMargin = 30 * time.Second

var hostileCases = []hostileCase{{
	// 403 storm: refusal is not transient; read fails immediately, no window to outlast.
	name:       "a source whose every segment is refused is named, not waited out",
	how:        refusesSegments,
	shape:      tsSegments,
	origin:     source.Origin{Segmented: true, Framing: media.FramingInBand},
	verdict:    watch.Dead,
	readFailed: true,
	tells:      []string{"403"},
	within:     probeBudget + read.BackoffMax + schedulingSlack,
}, {
	// Starving source: delivers slower than playback rate; only arithmetic convicts it.
	name:    "a source trickling under playback rate is refused before a renderer is pointed at it",
	how:     tricklesSegments,
	shape:   tsSegments,
	origin:  source.Origin{Segmented: true, Framing: media.FramingInBand},
	verdict: watch.Undeliverable,
	landed:  true,
	starved: true,
	within:  probeBudget + read.BackoffMax + schedulingSlack,
}, {
	// Tarpit: request accepted, no body; fMP4 has no mid-read deadline.
	name:    "a tarpit that never sends a body is ended by castor's own stall window",
	how:     acceptsAndSaysNothing,
	shape:   fmp4Segments,
	origin:  source.Origin{Segmented: true, Framing: media.FramingOutOfBand},
	verdict: watch.Stalled,
	silence: true,
	within:  probeBudget + watch.StallWindow + schedulingSlack,
}, {
	// Same tarpit with mid-read deadline; deadline notices but doesn't end the tarpit.
	name:    "the mid-read deadline notices a tarpit; castor's stall window is what ends it",
	how:     acceptsAndSaysNothing,
	shape:   wholeFile,
	origin:  source.Origin{},
	verdict: watch.Stalled,
	silence: true,
	tells:   []string{"Operation timed out"},
	within:  probeBudget + watch.StallWindow + schedulingSlack,
}}

// TestAHostileOriginIsRefusedRatherThanCast runs against each pathology in parallel.
func TestAHostileOriginIsRefusedRatherThanCast(t *testing.T) {
	t.Parallel()
	for _, tt := range hostileCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tl := newTools(t)

			origin := startHostile(t, tl, tt.shape, tt.how)
			if tt.how == tricklesSegments {
				// 4x not 2x: HLS demuxer opens next segment while reading current, doubling rate.
				if rate := origin.bytesPerMediaSecond(t); rate < 4*trickleBytesPerSecond {
					t.Fatalf("the fixture publishes %d bytes per media second over a %d B/s pipe, which is not a starving link",
						rate, trickleBytesPerSecond)
				}
			}

			program := hostileProgram(t, origin, tt.origin)
			readPlan := read.ForProgram(program, rwTimeout)
			policy := readPlan.Primary(program)
			// Assert deadline: two rows differ only in this, silent failures if table changes.
			t.Logf("read policy %q: deadline=%s backoff=%s pace=%.4gx", policy.Name, policy.Deadline, policy.Backoff, policy.Pace.Realtime)
			if tt.shape == fmp4Segments && policy.Deadline != 0 {
				t.Fatalf("this case is about a read given no mid-read deadline, and the policy carries %s", policy.Deadline)
			}
			if tt.shape != fmp4Segments && policy.Deadline == 0 {
				t.Fatal("this case is about a read that keeps its mid-read deadline, and the policy carries none")
			}

			dev := servedLocally()
			ctx, cancel := context.WithTimeout(t.Context(), tt.within)
			defer cancel()

			started := time.Now()
			out := newExecutor(castConfig(tl), dev, noStage).
				Run(ctx, attempt.Attempt{Try: 1, Program: program, Read: readPlan})
			ruled := time.Since(started)

			h := out.Evidence.Health
			t.Logf("castor ruled %s after %s: %s", out.Evidence.Verdict, ruled.Round(time.Second), h)

			// Bound assertion: outliving it means window grew or wiring failed, both defects.
			if out.Evidence.Cancelled {
				t.Fatalf("castor never ruled on this origin within %s, so nothing bounds it but the test's own patience", tt.within)
			}
			if out.Err == nil {
				t.Fatal("the cast reported success over an origin that never delivered a watchable stream")
			}
			if played := dev.snapshot(); len(played) > 0 {
				t.Errorf("a renderer was pointed at %q: a fault reached before playback is revisable, and pointing a renderer at it spends that", played)
			}
			if out.Evidence.Reached != attempt.PhaseReading {
				t.Errorf("reached %s, want %s: no renderer was ever handed a URL", out.Evidence.Reached, attempt.PhaseReading)
			}
			if out.Evidence.Verdict != tt.verdict {
				t.Errorf("verdict %s, want %s: %s", out.Evidence.Verdict, tt.verdict, h)
			}

			if tt.landed && h.Landed == 0 {
				t.Error("the verdict was reached with nothing in the buffer, so this case is judging silence rather than the rate it is about")
			}
			if !tt.landed && h.Landed != 0 {
				t.Errorf("the buffer holds %d bytes, so this case is judging a rate rather than the silence it is about", h.Landed)
			}
			if tt.starved {
				assertStarving(t, h)
			}
			if tt.silence && h.SinceGrowth <= watch.StallWindow {
				t.Errorf("nothing landed for %s, want more than the derived stall window %s: the verdict was reached before the window that licenses it",
					h.SinceGrowth.Round(time.Second), watch.StallWindow)
			}

			// Which party ended read: distinguishes source failure from castor giving up.
			switch {
			case tt.readFailed:
				if out.Evidence.ReadExit <= 0 {
					t.Errorf("the read reports exit status %d, want its own: this case is about a reader that failed at something it was doing",
						out.Evidence.ReadExit)
				}
				if out.Evidence.ReadErr == nil {
					t.Error("the outcome carries no terminal error for the read, so nothing above it can blame the source")
				}
			default:
				if out.Evidence.ReadExit > 0 {
					t.Errorf("the read reports exit status %d, so ffmpeg ended it and this case is not about castor's judgement",
						out.Evidence.ReadExit)
				}
				if h.Failed {
					t.Error("the verdict was reached over a read that had already failed, so the judgement under test is not what ended this cast")
				}
			}

			for _, tell := range tt.tells {
				if !mentions(out.Evidence.Lines, tell) {
					t.Errorf("the retained stderr never mentions %q, which is all a user has to act on:\n%s",
						tell, strings.Join(out.Evidence.Lines, "\n"))
				}
			}
		})
	}
}

// assertStarving verifies the arithmetic behind deliverability verdicts.
func assertStarving(t *testing.T, h watch.Health) {
	t.Helper()
	if h.Headroom <= 1 {
		t.Errorf("headroom %.4gx, want above realtime: a read that was never allowed to run ahead cannot be convicted of falling behind", h.Headroom)
	}
	if h.Samples == 0 {
		t.Error("the read never stated a speed, so the verdict rests on ffmpeg's opening run of N/A")
	}
	if h.Speed <= 0 || h.Speed >= 1 {
		t.Errorf("speed %.4gx, want a stated rate under playback rate", float64(h.Speed))
	}
	if h.SinceDeficit <= read.BackoffMax {
		t.Errorf("the deficit had lasted %s, want more than the reconnect ceiling %s the reader was handed: a judgement reached inside that window is reporting castor's own impatience",
			h.SinceDeficit.Round(time.Second), read.BackoffMax)
	}
}

// TestAStallInFlightEndsTheCastRatherThanRestartingIt verifies stalled casts stop cleanly.
func TestAStallInFlightEndsTheCastRatherThanRestartingIt(t *testing.T) {
	t.Parallel()
	tl := newTools(t)

	origin := startHostile(t, tl, fmp4Segments, stallsMidSegment)
	facts := source.Origin{Segmented: true, Framing: media.FramingOutOfBand}
	program := hostileProgram(t, origin, facts)
	readPlan := read.ForProgram(program, rwTimeout)
	policy := readPlan.Primary(program)
	if policy.Deadline != 0 {
		t.Fatalf("this case is about a read given no mid-read deadline, and the policy carries %s", policy.Deadline)
	}

	// Renderer must fetch; without this, verdict wrong before stall window is reached.
	dev := servedLocally()
	dev.drain, dev.played = true, make(chan string, 1)

	// Backstop timeout; cast returns on its own, timeout just prevents orphaned reader.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan attempt.Outcome, 1)
	go func() {
		done <- newExecutor(castConfig(tl), dev, noStage).
			Run(ctx, attempt.Attempt{Try: 1, Program: program, Read: readPlan})
	}()

	// Renderer signals on Play; stall window measured from playback start.
	select {
	case <-dev.played:
	case out := <-done:
		t.Fatalf("the cast ended before any renderer was pointed at anything: %v (verdict %s, %s)",
			out.Err, out.Evidence.Verdict, out.Evidence.Health)
	// Bounded: cast failing before playback fails case, not whole binary.
	case <-time.After(probeBudget + verdictMargin):
		t.Fatal("no renderer was pointed at the buffer, so this case never reached the window it is about")
	}
	playing := time.Now()

	// Verdict and return must both occur within stall window.
	var out attempt.Outcome
	select {
	case out = <-done:
		t.Logf("the cast returned on its own %s after playback started", time.Since(playing).Round(time.Second))
	case <-time.After(watch.StallWindow + verdictMargin):
		cancel()
		out = <-done
		t.Errorf("the cast reached its verdict and did not return until it was cancelled, %s after playback started: its encoder is parked reading a buffer nothing will grow, and the teardown is waiting for it",
			time.Since(playing).Round(time.Second))
	}
	t.Logf("castor ruled %s: %s", out.Evidence.Verdict, out.Evidence.Health)

	if out.Err == nil {
		t.Fatal("the cast reported success though its source stopped feeding it ten seconds into a minute of media")
	}
	if played := dev.snapshot(); len(played) != 1 {
		t.Fatalf("the renderer was played %d times, want 1: this case is about a fault reached with a viewer watching, and no receiver recovers from being pointed somewhere else mid-title",
			len(played))
	}
	if out.Evidence.Reached != attempt.PhasePlaying {
		t.Errorf("reached %s, want %s: the renderer had accepted the URL when the source went quiet", out.Evidence.Reached, attempt.PhasePlaying)
	}
	if out.Evidence.Verdict != watch.Stalled {
		t.Errorf("verdict %s, want %s: %s", out.Evidence.Verdict, watch.Stalled, out.Evidence.Health)
	}
	if h := out.Evidence.Health; h.SinceGrowth <= watch.StallWindow {
		t.Errorf("nothing landed for %s, want more than the derived stall window %s", h.SinceGrowth.Round(time.Second), watch.StallWindow)
	}
	if h := out.Evidence.Health; h.Landed == 0 || h.Handed == 0 {
		t.Errorf("the verdict was reached with %d bytes buffered and %d handed to the renderer, and this case is about a cast that WAS playing",
			h.Landed, h.Handed)
	}
	// Faults with renderer playing: attribute only, never re-cast (see watch.actions).
	var fault *watch.Fault
	if !errors.As(out.Err, &fault) {
		t.Fatalf("the cast failed with %v, which carries no verdict, so nothing above it can tell an abandoned cast from a revisable one", out.Err)
	}
	if fault.Revise {
		t.Error("the fault says the attempt may still be revised, which restarts a film someone is watching from the beginning")
	}
}

// TestAHealthyOriginPointsTheRendererAtTheBuffer is the control: tests healthy origin.
func TestAHealthyOriginPointsTheRendererAtTheBuffer(t *testing.T) {
	t.Parallel()
	tl := newTools(t)

	origin := startHostile(t, tl, tsSegments, servesEverything)
	program := hostileProgram(t, origin, source.Origin{Segmented: true, Framing: media.FramingInBand})
	readPlan := read.ForProgram(program, rwTimeout)

	dev := servedLocally()
	dev.played = make(chan string, 1)
	// Gate holds ~1.5s for speed samples; 60s timeout catches unbounded holds loudly.
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	done := make(chan attempt.Outcome, 1)
	go func() {
		done <- newExecutor(castConfig(tl), dev, noStage).
			Run(ctx, attempt.Attempt{Try: 1, Program: program, Read: readPlan})
	}()

	select {
	case at := <-dev.played:
		t.Logf("the renderer was pointed at %s", at)
	case out := <-done:
		t.Fatalf("the cast ended before any renderer was pointed at anything: %v (verdict %s, %s)",
			out.Err, out.Evidence.Verdict, out.Evidence.Health)
	case <-ctx.Done():
		t.Fatal("no renderer was pointed at the buffer over an origin that served everything, so the hostile cases prove nothing about hostility")
	}
	cancel()
	<-done
}

// servedLocally is renderer that never fetches; forces read-once, needed for health rules.
func servedLocally() *servedRenderer {
	return &servedRenderer{decodes: media.Capabilities{
		SelfFetch:       false,
		Video:           []media.VideoSupport{{Codec: media.CodecH264}},
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
		ServedContainer: media.MPEGTS,
	}}
}

// hostileProgram creates the normalized program for a hostile origin.
func hostileProgram(t *testing.T, o hostileOrigin, facts source.Origin) media.Program {
	t.Helper()
	program, err := media.NewProgram(media.Program{Inputs: []media.Input{{
		ID: media.PrimaryInputID, URL: mustURL(t, o.URL), ContentType: o.ContentType,
		Fetch: media.Fetch{Segmented: facts.Segmented, Framing: facts.Framing, Live: facts.Live},
	}}, Tracks: []media.TrackRef{
		{Input: media.PrimaryInputID, Kind: media.TrackVideo, Optional: true},
		{Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true},
	}, ClockInput: media.PrimaryInputID, EndPolicy: media.EndAtLongest})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

// mentions checks if retained lines contain want (prose only, not decision basis).
func mentions(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
