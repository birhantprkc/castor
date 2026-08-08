package watch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// TestWatchOpensOnTheArtifact is the loop's happy path: the facts are re-read until a rule
// says the wait may end, and nothing about it needs a producer at all.
func TestWatchOpensOnTheArtifact(t *testing.T) {
	var landed atomic.Int64
	go func() {
		time.Sleep(100 * time.Millisecond)
		landed.Store(4096)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := Watch(ctx, Monitor{Subject: "the stream output", Window: Opening, Landed: landed.Load}); err != nil {
		t.Fatalf("Watch: %v", err)
	}
}

// TestWatchCountsOnlyStatedSpeeds is what keeps the pre-playback hold from convicting a
// healthy read on ffmpeg's opening silence. Every progress field reads N/A until the first
// packet is muxed, and a real capture shows five consecutive blocks of it, so a hold that
// counted blocks would judge a link on four half-seconds of nothing.
func TestWatchCountsOnlyStatedSpeeds(t *testing.T) {
	producer := newFakeProducer()
	// Blocks arrive, carrying no speed: exactly ffmpeg's opening burst. Their bytes move,
	// so each one is a distinguishable block, and they are spaced past the polling interval
	// so a counter watching blocks rather than stated speeds would reach its threshold well
	// inside the window below.
	go func() {
		for i := range 8 {
			producer.report(media.Progress{Bytes: int64(i + 1)})
			time.Sleep(250 * time.Millisecond)
		}
	}()

	// The gate is held by the hold and nothing else: bytes have landed, no subtitles, and
	// the read was allowed twice realtime.
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	err := Watch(ctx, Monitor{
		Subject:  "playback gate",
		Window:   BeforePlay,
		Producer: producer,
		Landed:   func() int64 { return 33088 },
		Headroom: 2,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Watch = %v; a read that has stated no speed at all must neither open the gate nor be convicted by it", err)
	}
}

// TestWatchHoldsAStarvingReadUntilTheDeficitOutlastsTheBackoff is the observed failure
// driven through the loop, and what it pins is that the loop answers the run's falling
// speeds with neither of the two wrong answers.
//
// It must not open: those numbers are a renderer about to be pointed at a stream castor has
// already measured as unwatchable, which is the run that ended in bytes_sent=0. It must not
// refuse either, on a deficit a couple of seconds old: ffmpeg was handed a full reconnect
// ceiling to wait out a 429 and delivers nothing during it, and a whisper model loading on
// the goroutine draining the PCM tee freezes the same figure, so a refusal here walks and
// burns every other admitted link for a fault that need be about none of them.
//
// The conviction that arrives once the deficit really has outlasted that ceiling is asserted
// over the measurements, because sixty seconds is not something a loop test can wait out.
func TestWatchHoldsAStarvingReadUntilTheDeficitOutlastsTheBackoff(t *testing.T) {
	producer := newFakeProducer()
	go func() {
		// The observed run's numbers: one second of media, and a speed that only sinks. The
		// blocks are spaced past the polling interval, so each one is a sample the watch
		// really saw rather than a value it happened to catch.
		for _, speed := range []media.Speed{0.39, 0.159, 0.109, 0.0627, 0.05, 0.04, 0.03} {
			producer.report(media.Progress{Position: time.Second, Bytes: 33088, Speed: speed})
			time.Sleep(250 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err := Watch(ctx, Monitor{
		Subject:  "playback gate",
		Window:   BeforePlay,
		Producer: producer,
		Landed:   func() int64 { return 33088 },
		Headroom: 2,
	})

	var fault *Fault
	switch {
	case err == nil:
		t.Fatal("the gate opened on a read delivering a fraction of realtime")
	case errors.As(err, &fault):
		t.Fatalf("the gate refused a cast on a %s deficit, inside the reconnect ceiling its own read policy handed the reader: %v", fault.Health.SinceDeficit, err)
	case !errors.Is(err, context.DeadlineExceeded):
		t.Fatalf("Watch = %v, want the hold to end with the cast", err)
	}

	// And the same measurements, once the deficit has outlasted that ceiling, are a refusal
	// the attempt may still answer by changing the attempt: this is the one window where a
	// starving link can still be escaped, which is why the wait above is a hold and not an
	// opening.
	sustained := Health{Landed: 33088, Position: time.Second, Speed: 0.05, Headroom: 2, Samples: minSpeedSamples, SinceDeficit: deficitWindow + time.Second}
	rule, act, err := judge(BeforePlay, sustained)
	if err != nil {
		t.Fatal(err)
	}
	if rule.Kind != Undeliverable || act != revise {
		t.Errorf("a %s deficit is answered by rule %q with %s/%v, want a revisable undeliverable verdict", sustained.SinceDeficit, rule.Name, rule.Kind, act)
	}
}

// TestWatchCarriesTheProducersOwnError is the attribution property: a cast whose reader
// died must fail WITH that error rather than with a description of the stage that noticed.
// "encoder: spool producer failed: upstream pull: exit status 183" is what the alternative
// reads like.
// It carries the reader's own stderr with it for the same reason: a verdict about a
// producer that has nothing to be explained by is a stall report with no cause in it, and
// the lines exist nowhere else once castor has killed the process.
func TestWatchCarriesTheProducersOwnError(t *testing.T) {
	producer := newFakeProducer()
	producer.evidence = []string{"[hls] Failed to open segment 0 of playlist 0", "HTTP error 404 Not Found"}
	sentinel := errors.New("upstream pull: exit status 183")
	producer.settle(sentinel)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := Watch(ctx, Monitor{Subject: "playback gate", Window: BeforePlay, Producer: producer, Landed: func() int64 { return 1 }})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Watch = %v, want the reader's own error to survive to the caller", err)
	}
	var fault *Fault
	if !errors.As(err, &fault) || fault.Kind != Dead {
		t.Fatalf("Watch = %v, want a dead verdict", err)
	}
	if len(fault.Evidence) != len(producer.evidence) {
		t.Errorf("fault evidence = %q, want the reader's own stderr; without it a failed read has nothing to be explained by", fault.Evidence)
	}
}

// TestWatchGivesARendererItsGraceWindow is the other side of naming a renderer that never
// fetched: the window is a full reconnect ceiling, and a renderer taking a moment to ask
// for a URL it has just been handed must not be convicted for it. The verdict itself is
// pinned over Health, because no test waits out a minute.
func TestWatchGivesARendererItsGraceWindow(t *testing.T) {
	consumer := &fakeConsumer{}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err := Watch(ctx, Monitor{
		Subject:  "the playing cast",
		Window:   Playing,
		Producer: newFakeProducer(),
		Consumer: consumer,
		Landed:   func() int64 { return 8 << 20 },
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Watch = %v; a renderer inside its grace window must not be convicted", err)
	}
	if requests, _ := consumer.Fetched(); requests != 0 {
		t.Errorf("the watch reported %d fetches nobody made", requests)
	}
}

// TestWatchLeavesAPausedViewerTheCastTheyArePausing drives the pause through the real loop,
// which is where the port that answers it is read. A renderer that has taken nothing for
// longer than the stall window is the evidence a viewer standing up produces: it stops
// requesting segments, or stops draining the socket, and the sink sees silence either way.
//
// Neither reading of it ends the cast, and the second subtest is the one that matters. What
// castor still holds for a quiet renderer is not evidence about the renderer at all, so a
// verdict that fired once the figure looked empty was judging a viewer's pause against a
// number the encoder controls. Whether the renderer ever took what was made for it is settled
// after the cast, from bytes rather than from silence (see core.Undelivered). The window is
// not waited out here: the consumer states when it last fetched, which is exactly the fact
// production reads off both sinks.
func TestWatchLeavesAPausedViewerTheCastTheyArePausing(t *testing.T) {
	stopped := func() *fakeConsumer {
		return &fakeConsumer{requests: 3, last: time.Now().Add(-StallWindow - time.Second)}
	}

	for _, tt := range []struct {
		name      string
		delivered func() time.Duration
	}{{
		name:      "a pause over twenty fetchable minutes",
		delivered: func() time.Duration { return 20 * time.Minute },
	}, {
		name:      "a pause the delivery can say nothing about",
		delivered: nil,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			err := Watch(ctx, Monitor{
				Subject:   "the playing cast",
				Window:    Playing,
				Producer:  newFakeProducer(),
				Consumer:  stopped(),
				Landed:    func() int64 { return 8 << 20 },
				Delivered: tt.delivered,
			})
			var fault *Fault
			if errors.As(err, &fault) {
				t.Fatalf("a viewer who paused had their cast ended as %s on %s: %v", fault.Kind, fault.Health, err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Watch = %v, want the watch to keep watching a paused cast", err)
			}
		})
	}
}

// TestWatchErrorsWhenAWindowHasNoRule pins the table's error arm. It is unreachable by
// construction today (every window ends in a total row), and the point of the arm is that
// the day somebody removes one, the supervisor says so instead of quietly returning nil.
func TestWatchErrorsWhenAWindowHasNoRule(t *testing.T) {
	unknown := Window(99)
	if _, _, err := judge(unknown, Health{}); err == nil {
		t.Fatal("a window no rule answers resolved to a verdict")
	}
}
