// Package cast turns links into playback; device-agnostic pipeline with capability-driven shape.
package cast

import (
	"context"
	"slices"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/compose"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/source"
)

type Config struct {
	Source   *source.Resolver
	Delivery compose.DeliveryPreference

	// ReadDeadline is how long one upstream read may stall before it is retried.
	ReadDeadline time.Duration

	Execute execute.Config
}

// Play casts the ranked links, head first; the rest are what recovery switches to. turns hears each turn taken.
func Play(ctx context.Context, cfg Config, candidates []*source.Stream, turns attempt.Turns) error {
	return attempt.Cast(ctx, attempt.Intent{
		Candidates: slices.Clone(candidates),
		Deadline:   cfg.ReadDeadline,
		Delivery:   cfg.Delivery,
		Turns:      turns,
	}, execute.NewExecutor(cfg.Execute), cfg.Source)
}
