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

// UnreachableWindow is how long a renderer may go unanswered before it is gone, whatever each poll costs.
const UnreachableWindow = 2*60*time.Second + 30*time.Second

// AwaitPolledEnd polls renderer on interval until over, unanswered for unreachableFor, or ctx done.
func AwaitPolledEnd(ctx context.Context, name, question string, unreachableFor, interval time.Duration, poll func(context.Context) (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	answered := time.Now()
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
		over, err := poll(ctx)
		if err != nil {
			if silent := time.Since(answered); silent >= unreachableFor {
				// Last failure explains how (refused, timed out, no route).
				return &media.Gone{
					Renderer: name,
					Observed: fmt.Sprintf("%s went unanswered for %s", question, silent.Round(time.Second)),
					Err:      err,
				}
			}
			continue
		}
		answered = time.Now()
		if over {
			return nil
		}
	}
}
