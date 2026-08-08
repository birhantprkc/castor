package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stupside/castor/internal/cast/attempt"
)

// passthrough hands the renderer the source URL and lets it fetch the bytes directly. The
// renderer's own buffering handles pacing; castor touches none of the media, so there is
// nothing to encode and no cues to draw (there are no decoded frames to draw them into).
//
// This is also the one composition that asks the renderer to play with no delivery driver in
// between, so a refusal here is the renderer's own and is reported as such: castor read
// nothing, produced nothing and has nothing else to blame.
//
// It is the one composition with nothing at all past its Play call, which is why it needs no
// account of whether the renderer was playing: a refusal is a cast that never started, and a
// Play that succeeded is the cast delivered. Nothing here can be running while a viewer
// watches, because the renderer is fetching the source itself.
func passthrough(ctx context.Context, c *cast) landing {
	dev, err := c.renderer(ctx)
	if err != nil {
		return landing{err: err}
	}
	source := c.attempt.Source

	slog.InfoContext(ctx, "starting playback", "url", source.URL.String(), "content_type", source.ContentType)
	if err := dev.Play(ctx, source.URL, source.ContentType); err != nil {
		return landing{playErr: err, err: fmt.Errorf("starting playback: %w", err)}
	}
	slog.InfoContext(ctx, "playback handed off to device")
	return landing{reached: attempt.PhaseDelivered}
}
