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
	landed := func(ctx context.Context, err error) attempt.Outcome {
		return (&cast{evidence: attempt.Evidence{Reached: attempt.PhasePlaying}}).outcome(ctx, err)
	}

	t.Run("a cancelled cast is read from the context, not the error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		killed := errors.New("signal: killed")
		if !landed(ctx, killed).Evidence.Cancelled {
			t.Error("a cast killed by its own context does not report itself cancelled")
		}
		if landed(t.Context(), killed).Evidence.Cancelled {
			t.Error("a cast whose context is live reports itself cancelled")
		}
	})

	t.Run("an undelivered stream", func(t *testing.T) {
		short := &watch.Undelivered{Handed: 2 * time.Minute, Produced: 2 * time.Hour}
		if out := landed(t.Context(), fmt.Errorf("delivering the stream: %w", short)); out.Evidence.Undelivered != short {
			t.Errorf("evidence Undelivered = %v, want the delivery's own account", out.Evidence.Undelivered)
		}
	})

	t.Run("a renderer that went away", func(t *testing.T) {
		gone := &media.Gone{Renderer: "Living Room TV", Err: errors.New("no route to host")}
		out := landed(t.Context(), errors.Join(gone, fmt.Errorf("delivering the stream: %w", context.Canceled)))
		if out.Evidence.RendererGone != gone {
			t.Error("the evidence does not carry the renderer's own account")
		}
	})

	t.Run("a watch verdict", func(t *testing.T) {
		verdict := &watch.Fault{Kind: watch.Dead, Health: watch.Health{Speed: 0.159}, Evidence: []string{"404"}}
		out := landed(t.Context(), verdict)
		if out.Evidence.Verdict != watch.Dead || out.Evidence.Health.Speed != 0.159 || len(out.Evidence.Lines) != 1 {
			t.Errorf("evidence = %+v, want the verdict, its measurements and its lines", out.Evidence)
		}
	})
}
