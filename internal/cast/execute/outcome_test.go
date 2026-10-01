package execute

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// TestTheOutcomeCarriesWhatTheRecoveryLoopClassifies folds each typed ending into the evidence.
func TestTheOutcomeCarriesWhatTheRecoveryLoopClassifies(t *testing.T) {
	playing := ran{reached: attempt.PhasePlaying}

	t.Run("an undelivered stream", func(t *testing.T) {
		short := &watch.Undelivered{Handed: 2 * time.Minute, Produced: 2 * time.Hour}
		if e := evidence(playing, fmt.Errorf("delivering the stream: %w", short), false); e.Undelivered != short {
			t.Errorf("evidence Undelivered = %v, want the delivery's own account", e.Undelivered)
		}
	})

	t.Run("a renderer that went away", func(t *testing.T) {
		gone := &media.Gone{Renderer: "Living Room TV", Err: errors.New("no route to host")}
		if e := evidence(playing, errors.Join(gone, fmt.Errorf("delivering the stream: %w", context.Canceled)), false); e.RendererGone != gone {
			t.Error("the evidence does not carry the renderer's own account")
		}
	})

	t.Run("a watch verdict", func(t *testing.T) {
		verdict := &watch.Fault{Kind: watch.Dead, Health: watch.Health{Speed: 0.159}, Evidence: []string{"404"}}
		if e := evidence(playing, verdict, false); e.Verdict != watch.Dead || e.Health.Speed != 0.159 || len(e.Lines) != 1 {
			t.Errorf("evidence = %+v, want the verdict, its measurements and its lines", e)
		}
	})
}

// The evidence is read off how the pipeline ended, never off flags a step set on the way.
func TestTheEvidenceIsWhatThePipelineReturned(t *testing.T) {
	refusal := errors.New("SOAP 714")
	reader := &pull{done: make(chan struct{})}
	for _, tt := range []struct {
		name string
		r    ran
		err  error
		want func(attempt.Evidence) bool
	}{
		{"a cast that ended cleanly was delivered", ran{reached: attempt.PhasePlaying}, nil,
			func(e attempt.Evidence) bool { return e.Reached == attempt.PhaseDelivered }},
		{"a renderer refusing the source itself was handed it", ran{}, &playRefused{err: refusal, source: true},
			func(e attempt.Evidence) bool { return e.Handoff && errors.Is(e.PlayErr, refusal) }},
		{"a renderer refusing what castor serves was not handed the source", ran{reached: attempt.PhaseOpening}, &playRefused{err: refusal},
			func(e attempt.Evidence) bool { return !e.Handoff && e.PlayErr != nil }},
		{"a timeline castor could not read is named", ran{}, &timelineUnreadable{err: refusal},
			func(e attempt.Evidence) bool { return errors.Is(e.TimelineErr, refusal) }},
		{"a read that reached its delivery served a proven buffer", ran{reached: attempt.PhaseOpening, reader: reader}, errors.New("stalled"),
			func(e attempt.Evidence) bool { return e.Buffered && e.Reached == attempt.PhaseOpening }},
		{"a read stopped at its gate served nothing", ran{reached: attempt.PhaseReading, reader: reader}, errors.New("dead"),
			func(e attempt.Evidence) bool { return !e.Buffered }},
		{"a remux that reached its delivery buffered nothing", ran{reached: attempt.PhaseOpening}, errors.New("stalled"),
			func(e attempt.Evidence) bool { return !e.Buffered }},
	} {
		if e := evidence(tt.r, tt.err, false); !tt.want(e) {
			t.Errorf("%s: evidence = %+v", tt.name, e)
		}
	}
}

// Cancellation is read from the context the attempt ran under, not from how its error reads.
func TestACancelledAttemptSaysSo(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx  context.Context
		want bool
	}{{ctx, true}, {t.Context(), false}} {
		dev := &fakeDevice{caps: chromecastLike(media.MP4)}
		out := NewExecutor(Config{MaxHeight: 1080, Renderer: rendererOf(selfFetching(), dev), Timelines: direct{}}).Run(tc.ctx,
			attempt.Attempt{Program: programFromStream(t, passthroughCandidate())})
		if out.Evidence.Cancelled != tc.want {
			t.Errorf("Cancelled = %v under a context cancelled %v", out.Evidence.Cancelled, tc.want)
		}
	}
}
