package e2e

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/pipeline"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/resolve"
)

// What castor does about a hostile origin, over the real executor.
//
// Everything asserted here is castor's OWN behaviour and never ffmpeg's: which verdict it
// reached, on which measurements, how long it took to reach it, and above all that no
// renderer was ever pointed at anything. The last one is the whole point of the pre-playback
// window: past a Play call no revision exists (see attempt.Phase), so a source castor could
// have refused becomes a failure a viewer watches instead of one an operator is told about.
//
// The wall clock here is real and it is derived, not chosen. Two windows dominate: one
// reconnect ceiling before a measured deficit convicts a read (watch's deficitWindow, which
// is read.BackoffMax), and two ceilings plus a margin before silence does (watch.StallWindow).
// Both are derived from the backoff castor itself hands the reader, because a judgement that
// fires inside the backoff it granted is not observing a stall, it is causing one. There is
// no shortening them from a test without testing something else, so each case states its
// bound as the sum of the windows it must outlast and the cases run in parallel, which makes
// the suite's added cost the longest of them rather than their sum.

// hostileCase is one origin pathology and the answer castor must reach about it.
type hostileCase struct {
	name  string
	how   hostility
	shape hostileShape

	// origin is what source resolution would have established about this source, and it is
	// the only input to the read policy. This is where a case states whether the mid-read
	// deadline applies, because that is the question read.For answers, and it is the axis two
	// of these cases exist to separate.
	origin media.Origin

	// verdict is the verdict castor must reach, and it is the assertion this suite is for: a
	// rule verified against a hand-built Health says nothing about whether the shipping
	// wiring ever reaches it.
	verdict watch.Kind

	// landed states whether media must have reached the buffer before the verdict. It is not
	// decoration: it is the difference between a read judged on a measured rate and a read
	// judged on silence, and those are two different rows of the health table.
	landed bool

	// starved requires the verdict to carry a measured deficit: a stated speed under playback
	// rate, against a pace above realtime, sustained past the window that licenses acting on
	// it. Without these numbers the refusal is an opinion.
	starved bool

	// silence requires the verdict to carry a producer that delivered nothing for the derived
	// stall window.
	silence bool

	// readFailed requires the READER's own terminal failure rather than castor's judgement of
	// a reader that was still running. The discriminator is the exit status: positive is a
	// read that failed at something it was doing, negative is one castor killed, and castor
	// kills the reader on every fault it names itself.
	readFailed bool

	// tells are substrings the retained stderr must carry. They are the material a user acts
	// on, and they exist nowhere else: castor kills the reader, so its own error path never
	// runs and these lines are only ever seen because the fault carried them out.
	tells []string

	// within is the wall-clock bound, derived per row from the windows the case has to
	// outlast. It is also the case's context, so a bound that is missed reports "never ruled"
	// rather than an unrelated assertion failure.
	within time.Duration
}

// probeBudget is what core.Measure gives the source probe before a cast proceeds on nothing
// known. It is not configurable and it is not skippable, so every case that tarpits the
// probe pays it in full before its read even starts, and every bound below carries it.
const probeBudget = read.BackoffMax / 2

// verdictMargin is how much longer than the window itself a verdict is allowed to take. It
// covers the watch's polling cadence (a fifth of a second) and the ten media seconds a
// stalling origin serves before it freezes, and nothing else: everything else about these
// bounds is a window castor derived, and padding them would be padding the claim.
const verdictMargin = 30 * time.Second

var hostileCases = []hostileCase{{
	// The 403 storm, and the one case that has to be FAST: a refusal is not a transient
	// answer, so nothing here should be waited out. The reader gives up on each segment after
	// its open retries and then reaches a terminal error of its own, which the playback gate
	// reads as a dead read and refuses while the attempt can still be changed.
	//
	// Bound: the probe budget, which a dead playlist may legitimately spend in full (ffmpeg
	// answers one whose segments all refuse by walking every segment, and a real one measured
	// 199 seconds of that before the budget existed), plus one reconnect ceiling. Measured at
	// under a second for a sixty-segment playlist over loopback, and the bound is what says a
	// refusal may never be waited out the way silence is: there is no window to outlast here.
	name:       "a source whose every segment is refused is named, not waited out",
	how:        refusesSegments,
	shape:      tsSegments,
	origin:     media.Origin{Segmented: true, Framing: media.FramingInBand},
	verdict:    watch.Dead,
	readFailed: true,
	tells:      []string{"403"},
	within:     probeBudget + read.BackoffMax,
}, {
	// The starving upstream, which is the failure the whole deliverability judgement was
	// written for and the one it had never been exercised against: a link delivering media
	// slower than it will be played, measured as ffmpeg's own speed against the pace the read
	// was allowed. The field casts that died measured 0.0627, 0.109, 0.159 and 0.39 against a
	// readrate of 2.0; this origin reproduces that ratio with a 24 KB/s pipe.
	//
	// Nothing is wrong with the media, nothing answers an error, and the read never fails: the
	// only thing against this cast is arithmetic, which is why nothing before this judgement
	// existed could see it at all.
	//
	// Bound: the probe budget (the probe reads the same trickle and answers nothing), plus one
	// reconnect ceiling of continuous deficit before the pre-playback arm may act, plus the
	// startup lag the confidence window covers, plus a margin.
	name:    "a source trickling under playback rate is refused before a renderer is pointed at it",
	how:     tricklesSegments,
	shape:   tsSegments,
	origin:  media.Origin{Segmented: true, Framing: media.FramingInBand},
	verdict: watch.Undeliverable,
	landed:  true,
	starved: true,
	within:  probeBudget + read.BackoffMax + 60*time.Second,
}, {
	// The tarpit: the segment request accepted, headers sent, no body ever. Nothing is
	// measured, because nothing was ever muxed, so no rate exists to convict and the only fact
	// about this cast is that nothing has arrived. That is the stall row, and it is the row the
	// fragile read policy's whole safety argument rests on: this source is fMP4, so the read
	// carries no mid-read deadline at all, and withholding one is only sound because this
	// window exists and is reached.
	//
	// Bound: the probe budget plus two reconnect ceilings and the margin (watch.StallWindow),
	// which is the longest wait in castor and is derived rather than picked: one full backoff
	// that fails, a second that succeeds, and time for its bytes to arrive.
	name:    "a tarpit that never sends a body is ended by castor's own stall window",
	how:     acceptsAndSaysNothing,
	shape:   fmp4Segments,
	origin:  media.Origin{Segmented: true, Framing: media.FramingOutOfBand},
	verdict: watch.Stalled,
	silence: true,
	within:  probeBudget + watch.StallWindow + 60*time.Second,
}, {
	// The same tarpit on the row that KEEPS the configured mid-read deadline, and the answer is
	// the same window, which is worth a case of its own because it is not what the deadline
	// reads like it does.
	//
	// Measured against this origin: a whole-file read given -rw_timeout 30s with castor's
	// reconnect terms does not fail at thirty seconds. Each timeout is followed by a reconnect
	// that stalls again, at backoffs of 0, 1, 3, 7, 15 then 31 seconds ("Will reconnect at 0 in
	// 15 second(s), error=Operation timed out"), and the read finally gave up after 268 seconds
	// with "Error opening input: Input/output error". So the deadline NOTICES a tarpit and does
	// not end one: on both read policies the bound that actually ends the cast is castor's own
	// stall window, and the difference the deadline makes to a tarpit is a stderr tail that
	// names it rather than the silence the fragile row leaves behind.
	//
	// Bound: as the fragile tarpit above, and deliberately shorter than the 268 seconds ffmpeg
	// would have taken, because a case that let the reader die first would be asserting
	// ffmpeg's behaviour instead of castor's.
	name:    "the mid-read deadline notices a tarpit; castor's stall window is what ends it",
	how:     acceptsAndSaysNothing,
	shape:   wholeFile,
	origin:  media.Origin{},
	verdict: watch.Stalled,
	silence: true,
	tells:   []string{"Operation timed out"},
	within:  probeBudget + watch.StallWindow + 60*time.Second,
}}

// TestAHostileOriginIsRefusedRatherThanCast runs castor's real executor against each
// pathology and asserts what castor concluded.
//
// The cases run in parallel, and so does this test itself, because their cost is waiting
// rather than working: each holds one ffmpeg on a socket delivering a trickle or nothing at
// all. Without t.Parallel here the whole group would run before the other parallel tests in
// this package rather than alongside them, which turns the suite's added cost from the longest
// case into the sum of two groups.
func TestAHostileOriginIsRefusedRatherThanCast(t *testing.T) {
	t.Parallel()
	for _, tt := range hostileCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tl := newTools(t)

			origin := startHostile(t, tl, tt.shape, tt.how)
			if tt.how == tricklesSegments {
				// The premise of the trickle, checked against the fixture actually encoded rather
				// than assumed from the bitrate asked for: the origin has to deliver well under
				// playback rate, because the floor the deliverability rule compares against is
				// playback itself. A fixture that compressed smaller would be perfectly deliverable
				// over the same pipe and this case would assert nothing.
				//
				// FOUR TIMES and not merely above it, because a segment fetch is not one connection:
				// ffmpeg's HLS demuxer opens the next segment while it is still reading the current
				// one, so the media arrives at twice the pipe (measured: a 24 KB/s trickle delivered
				// 48 KB/s and the read reported 0.42x rather than the 0.21x the pipe alone predicts).
				// The factor keeps the margin under playback rate with that doubling in it.
				if rate := origin.bytesPerMediaSecond(t); rate < 4*trickleBytesPerSecond {
					t.Fatalf("the fixture publishes %d bytes per media second over a %d B/s pipe, which is not a starving link",
						rate, trickleBytesPerSecond)
				}
			}

			policy, err := read.For(read.ShapeOf(tt.origin), rwTimeout)
			if err != nil {
				t.Fatal(err)
			}
			// The read policy is the case's premise, so the case states it: two of these rows differ
			// only in whether ffmpeg was given a mid-read deadline, and a table that merely hoped
			// for that would silently stop testing it the day the read table changed.
			t.Logf("read policy %q: deadline=%s backoff=%s pace=%.4gx", policy.Name, policy.Deadline, policy.Backoff, policy.Pace.Realtime)
			if tt.shape == fmp4Segments && policy.Deadline != 0 {
				t.Fatalf("this case is about a read given no mid-read deadline, and the policy carries %s", policy.Deadline)
			}
			if tt.shape != fmp4Segments && policy.Deadline == 0 {
				t.Fatal("this case is about a read that keeps its mid-read deadline, and the policy carries none")
			}

			dev := &servedRenderer{}
			ctx, cancel := context.WithTimeout(t.Context(), tt.within)
			defer cancel()

			started := time.Now()
			out := pipeline.NewExecutor(hostileConfig(tl), connectTo(dev), "127.0.0.1").
				Run(ctx, attempt.Attempt{Try: 1, Source: hostileStream(t, origin), Read: policy})
			ruled := time.Since(started)

			h := out.Evidence.Health
			t.Logf("castor ruled %s after %s: %s", out.Evidence.Verdict, ruled.Round(time.Second), h)

			// The bound is an assertion and not a convenience. Every window here is derived from the
			// backoff castor hands the reader, so a case that outlives its bound means either a
			// window has grown or the wiring never reached the rule at all, and both are the defect
			// this suite exists to catch.
			if out.Evidence.Cancelled {
				t.Fatalf("castor never ruled on this origin within %s, so nothing bounds it but the test's own patience", tt.within)
			}
			if out.Err == nil {
				t.Fatal("the cast reported success over an origin that never delivered a watchable stream")
			}
			if played := dev.snapshot(); len(played) > 0 {
				t.Errorf("a renderer was pointed at %q: a fault reached before playback is revisable, and pointing a renderer at it spends that", played)
			}
			if out.Reached() != attempt.PhaseReading {
				t.Errorf("reached %s, want %s: no renderer was ever handed a URL", out.Reached(), attempt.PhaseReading)
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

			// Which party ended the read, which is the whole of what separates a source that failed
			// from a source castor gave up on. Both are real answers and each case is about exactly
			// one of them.
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

// assertStarving checks the arithmetic behind a deliverability verdict, because that verdict
// is nothing but arithmetic: without these four numbers it is an accusation against an origin
// with no measurement attached, and it is expensive (before playback it walks and re-touches
// every other admitted link).
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

// TestAStallInFlightEndsTheCastRatherThanRestartingIt is the other side of the fragile read's
// trade, and the only case in castor that reaches its in-flight window over a real origin.
//
// The origin serves ten media seconds and then holds one fragment half written, forever. By
// then the read has stream info, a proven rate and a buffer, so the gate opens and a renderer
// is pointed at the stream: this is not a source that can be refused, it is a cast someone is
// watching that has stopped being fed. Nothing before the supervisor existed could see it at
// all, because the executor touched the reader for the last time on its way to Play, so a read
// that died after playback began ran to the end of the title and the cast reported success.
//
// The renderer is held to what the read table promises here: the source is fMP4, so the read
// carries no mid-read deadline (the alternative manufactures a fragment nothing can
// resynchronise, at "Invalid NAL unit size" and exit 183), which means no ffmpeg timer will
// ever end this. Castor's stall window is the whole bound, and this is where it is spent with
// a viewer in front of it.
//
// And it must not be cast again. A renderer holds the URL, so the fault is past the line a
// recovery may cross, and a second attempt is a fresh work directory, a fresh connect and a
// fresh Play: the film from the beginning at minute ten.
//
// WHAT THIS CASE FOUND, and the reason it cancels the cast instead of waiting for it to end:
// castor reaches the verdict on time and then does not return. Measured here, with the stack
// captured while it was hung: the verdict landed 154 seconds in ("cast abandoned ... window=playing
// verdict=stalled revisable=false"), and Executor.Run stayed inside the delivery's teardown for
// another 5 minutes 27 seconds, until the CALLER's context expired. The stream delivery's
// teardown closes the server and then waits for the encoder (core/deliver.go:496, through
// finishEncoder), and nothing has killed that encoder: the cast's own context is cancelled by a
// defer in pipeline.run, which cannot run until the leg returns, and the leg is inside the wait.
// The encoder is parked reading a buffer that will never grow again, so the wait never ends. The
// segmented delivery's teardown kills its process first (core/deliver.go:595) and does not have
// this shape. In production the cast context ends only on Ctrl+C or SIGTERM (main.go), so a
// supervised cast that stalls prints its fault and then hangs indefinitely.
//
// So this case asserts what castor does establish (the verdict, its measurements and its
// refusal to revise) and states the timing claim against the verdict rather than against the
// return, which is why it cancels rather than waiting. Fixing the teardown is not this phase.
func TestAStallInFlightEndsTheCastRatherThanRestartingIt(t *testing.T) {
	t.Parallel()
	tl := newTools(t)

	origin := startHostile(t, tl, fmp4Segments, stallsMidSegment)
	policy, err := read.For(read.ShapeOf(media.Origin{Segmented: true, Framing: media.FramingOutOfBand}), rwTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Deadline != 0 {
		t.Fatalf("this case is about a read given no mid-read deadline, and the policy carries %s", policy.Deadline)
	}

	// It fetches what it is served, and that is required rather than realistic dressing: a
	// renderer that took nothing is convicted by its own rule (unfetched, at one reconnect
	// ceiling) long before the stall window is reached, and the case would pass while asserting
	// the wrong verdict entirely.
	dev := &servedRenderer{drain: true, played: make(chan string, 1)}

	// A backstop and not a schedule: it has to outlast the whole case, including the teardown
	// this cast does not come back from on its own, so nothing is asserted by reaching it.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan attempt.Outcome, 1)
	go func() {
		done <- pipeline.NewExecutor(hostileConfig(tl), connectTo(dev), "127.0.0.1").
			Run(ctx, attempt.Attempt{Try: 1, Source: hostileStream(t, origin), Read: policy})
	}()

	// Wait to be told rather than polling: the renderer signals on Play, so the stall window is
	// measured from the instant the cast became one somebody is watching, which is the instant
	// the origin's last fragment lands and growth stops.
	select {
	case <-dev.played:
	case out := <-done:
		t.Fatalf("the cast ended before any renderer was pointed at anything: %v (verdict %s, %s)",
			out.Err, out.Evidence.Verdict, out.Evidence.Health)
	// Bounded so that a cast which never gets as far as playback fails this case rather than
	// the whole binary: the wait is the source probe's budget plus the margin, and a gate that
	// opens at all opens within a few seconds of the first byte (it holds for a derived number
	// of speed samples and nothing else).
	case <-time.After(probeBudget + verdictMargin):
		t.Fatal("no renderer was pointed at the buffer, so this case never reached the window it is about")
	}
	playing := time.Now()

	// The verdict has to be reached within the window that licenses it, plus a margin for the
	// watch's own polling cadence. The cast is then cancelled, because it will not return by
	// itself (see the teardown finding above) and the verdict is already on the outcome either
	// way: a cancellation cannot manufacture one, since Verdict and Health are filled in from a
	// fault or not at all.
	var out attempt.Outcome
	select {
	case out = <-done:
		t.Logf("the cast returned on its own %s after playback started", time.Since(playing).Round(time.Second))
	case <-time.After(watch.StallWindow + verdictMargin):
		cancel()
		out = <-done
		t.Logf("the cast had to be cancelled %s after playback started; it had already reached its verdict",
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
	if out.Reached() != attempt.PhasePlaying {
		t.Errorf("reached %s, want %s: the renderer had accepted the URL when the source went quiet", out.Reached(), attempt.PhasePlaying)
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
	// A fault reached with a renderer playing may only be attributed, never answered by casting
	// again, and this is where that is decided rather than in the loop above (see watch.actions).
	var fault *watch.Fault
	if !errors.As(out.Err, &fault) {
		t.Fatalf("the cast failed with %v, which carries no verdict, so nothing above it can tell an abandoned cast from a revisable one", out.Err)
	}
	if fault.Revise {
		t.Error("the fault says the attempt may still be revised, which restarts a film someone is watching from the beginning")
	}
}

// TestAHealthyOriginPointsTheRendererAtTheBuffer is the control, and it is what the four
// cases above cannot do without: every one of them passes just as well over a fixture castor
// could never have cast at all. This one takes the same fixture and the same renderer over an
// origin that behaves, and requires that a renderer IS pointed at the buffer.
//
// It ends by cancelling rather than by running out of input: the point is reached the instant
// the gate opens and a URL is handed over, and playing out a minute of media past that would
// buy nothing but a minute.
func TestAHealthyOriginPointsTheRendererAtTheBuffer(t *testing.T) {
	t.Parallel()
	tl := newTools(t)

	origin := startHostile(t, tl, tsSegments, servesEverything)
	policy, err := read.For(read.ShapeOf(media.Origin{Segmented: true, Framing: media.FramingInBand}), rwTimeout)
	if err != nil {
		t.Fatal(err)
	}

	dev := &servedRenderer{played: make(chan string, 1)}
	// The gate's own hold is what this bound is about: it holds for the derived number of
	// speed samples before it lets a healthy read through (decision 3's one user-visible
	// latency change, costed at roughly 1.5s), so a bound of a minute is generous by more than
	// an order of magnitude and still fails loudly if the hold ever became unbounded.
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	done := make(chan attempt.Outcome, 1)
	go func() {
		done <- pipeline.NewExecutor(hostileConfig(tl), connectTo(dev), "127.0.0.1").
			Run(ctx, attempt.Attempt{Try: 1, Source: hostileStream(t, origin), Read: policy})
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

// servedRenderer is a renderer that never fetches for itself, which is what puts every case
// here on the read-once composition: the only one with a reader of castor's own behind a
// buffer, and therefore the only one whose source a health rule can judge at all.
//
// It records what it was pointed at and never fetches it. Fetching would only matter to a
// case that gets as far as being served, and the four hostile cases assert the opposite.
type servedRenderer struct {
	// played, when set, receives every URL handed over, so the control case can proceed the
	// instant playback starts rather than polling for it.
	played chan string

	// drain makes it fetch what it is served, on a goroutine of its own so that Play returns
	// the way a real renderer's does. A case about what happens WHILE a renderer is playing
	// needs both halves: a Play that only returned at the end of the title would put every
	// later fault after the cast rather than during it, and a renderer that took nothing is
	// convicted by its own rule before any other window is reached.
	drain bool

	mu    sync.Mutex
	plays []string
}

// Compile-time proof the stand-in is what the executor consumes.
var _ device.Device = (*servedRenderer)(nil)

func (d *servedRenderer) Play(ctx context.Context, streamURL *url.URL, _ string) error {
	d.mu.Lock()
	d.plays = append(d.plays, streamURL.String())
	d.mu.Unlock()
	if d.played != nil {
		select {
		case d.played <- streamURL.String():
		default: // a case that has stopped listening must not wedge the cast
		}
	}
	if d.drain {
		go d.fetch(ctx, streamURL.String())
	}
	return nil
}

// fetch takes the served stream the way a renderer does, and throws it away. It is bounded by
// the cast's own context, so a producer that wedges fails a case rather than hanging it.
func (d *servedRenderer) fetch(ctx context.Context, streamURL string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

func (d *servedRenderer) Capabilities() media.Renderer {
	return media.Renderer{
		Video:           []media.VideoSupport{{Codec: media.CodecH264}},
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
		ServedContainer: media.MPEGTS,
	}
}

func (d *servedRenderer) StreamHeaders(string) map[string]string { return nil }
func (d *servedRenderer) Close() error                           { return nil }

func (d *servedRenderer) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.plays...)
}

// connectTo hands the executor a renderer instead of discovering one, which is what keeps
// these cases about the source: discovery and device protocols are covered by their own
// suites and neither changes a byte of what a hostile origin delivers.
func connectTo(dev device.Device) pipeline.ConnectFunc {
	return func(context.Context, core.Config) (device.Device, error) { return dev, nil }
}

// rwTimeout is the configured mid-read deadline, at the value castor ships. It is the one
// term of a read an operator still owns, and one of these cases is about the row that keeps
// it while another is about the row that refuses it, so it is production's number rather than
// a convenient one.
const rwTimeout = 30 * time.Second

// hostileConfig is the configuration these casts run on: a renderer family that never
// fetches for itself, the real media tools, and the shipped timeouts.
func hostileConfig(tl tools) core.Config {
	return core.Config{
		Device:    core.DeviceConfig{Type: device.TypeDLNA},
		Transcode: core.TranscodeConfig{FFmpegPath: tl.ffmpeg, RWTimeout: rwTimeout},
		Resolver: resolve.Config{
			FFprobePath: tl.ffprobe,
			MaxHeight:   1080,
			// Required rather than optional, here as in production: a zero value is an expired
			// deadline rather than an absent one, so every probe would fail instantly and every
			// copy decision would silently fall back.
			ProbeTimeout: 30 * time.Second,
		},
	}
}

// hostileStream is the media.Stream a cast resolves to for a hostile origin.
func hostileStream(t *testing.T, o hostileOrigin) *media.Stream {
	t.Helper()
	return &media.Stream{URL: mustURL(t, o.URL), ContentType: o.ContentType}
}

// mentions reports whether any retained line mentions want. The lines are prose, so a case
// may only look for the number or the phrase a user would search for, and may never key a
// decision on one (see attempt.Evidence.Lines).
func mentions(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
