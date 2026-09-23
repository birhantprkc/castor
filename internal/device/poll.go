package device

import (
	"context"
	"fmt"
	"time"

	"github.com/stupside/castor/internal/media"
)

// Polls renderers on cadence (protocol-agnostic logic; both UPnP/Roku use this).
const (
	// Cadence and budget are separate: slow renderer (buffering/probing) != absent (1 connection limit).
	PollInterval = 2 * time.Second
	PollTimeout  = 10 * time.Second
)

// unreachableWindow and UnreachablePolls bound consecutive failures forgiven before gone.
const (
	unreachableWindow = 2*60*time.Second + 30*time.Second
	UnreachablePolls  = int(unreachableWindow / PollInterval)
)

// AwaitPolledEnd polls renderer on interval until over, failed unreachableAfter times, or ctx done.
func AwaitPolledEnd(ctx context.Context, name, question string, unreachableAfter int, interval time.Duration, poll func(context.Context) (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
		over, err := poll(ctx)
		if err != nil {
			missed++
			if missed >= unreachableAfter {
				// Last failure explains how (refused, timed out, no route).
				return &media.Gone{
					Renderer: name,
					Observed: fmt.Sprintf("%d consecutive %s polls over %s went unanswered",
						unreachableAfter, question, time.Duration(unreachableAfter)*interval),
					Err: err,
				}
			}
			continue
		}
		missed = 0
		if over {
			return nil
		}
	}
}
