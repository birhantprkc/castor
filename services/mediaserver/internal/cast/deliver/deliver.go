// Package deliver is how a cast reaches its device: a progressive stream or a live HLS directory, the spool its bytes pass through, and the terms both open on.
package deliver

import (
	"context"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/container"
)

// settleInterval is how often a delivery's Wait re-reads whether it still has anything to do.
const settleInterval = 500 * time.Millisecond

// Artifact is what a delivery hands the device, which the watch that opens it waits on.
type Artifact struct {
	// Subject names it in a log line and in a fault ("the stream output", "the HLS playlist").
	Subject string

	Landed func() int64

	Grace time.Duration
}

// Opening is everything a delivery mechanism is opened with.
type Opening struct {
	Format    container.Format
	Listeners Listeners
	Headers   map[string]string

	// IdleGrace is how long an idle device is waited for before the delivery counts as done.
	IdleGrace time.Duration

	// WriteDeadline bounds one socket write, so a hung client cannot hold its goroutine.
	WriteDeadline time.Duration
}

// settle waits for drained, then until finished holds, re-reading it every settleInterval.
func settle(ctx context.Context, drained <-chan struct{}, finished func() bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-drained:
	}
	tick := time.NewTicker(settleInterval)
	defer tick.Stop()
	for !finished() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	return nil
}
