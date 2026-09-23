package execute

import (
	"context"
	"errors"

	"github.com/stupside/castor/internal/cast/policy/watch"
)

func (c *cast) readMonitor(m watch.Monitor) watch.Monitor {
	m.Producer = c.reader
	m.Telemetry = c.reader
	m.Landed = c.spool.Size
	m.Headroom = c.reader.judgedPace()
	return m
}

// lead is nil where this cast runs no transcription, so no readiness rule waits on one.
func (c *cast) lead() watch.Lead {
	if c.burn == nil {
		return nil
	}
	return c.burn
}

func (c *cast) playable(ctx context.Context) error {
	return watch.Watch(ctx, c.readMonitor(watch.Monitor{
		Subject: "playback gate",
		Window:  watch.BeforePlay,
		Lead:    c.lead(),
	}))
}

func (c *cast) playingMonitor(aud watch.Audience) watch.Monitor {
	m := watch.Monitor{
		Subject:  "the playing cast",
		Window:   watch.Playing,
		Audience: aud,
	}
	if c.row.Kind.Buffers() {
		return c.readMonitor(m)
	}
	m.Producer = encoderOutput{proc: c.proc, ended: c.sink.Drained()}
	m.Landed = c.sink.Artifact().Landed
	return m
}

func (c *cast) supervising(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	delivered := make(chan error, 1)
	go func() { delivered <- c.sink.Wait(ctx) }()
	playback := make(chan error, 1)
	go func() { playback <- c.dev.AwaitEnd(ctx) }()

	// Nil where delivery has no supervisor; sink deleting behind its live edge reads as stall on every cast.
	var judged chan error
	if aud := c.sink.Audience(); aud != nil {
		judged = make(chan error, 1)
		go func() { judged <- watch.Watch(ctx, c.playingMonitor(aud)) }()
	}

	select {
	case err := <-delivered:
		// A Wait that ended cleanly is asked one more question once this returns (see settled).
		c.ran = err == nil
		cancel()
		return errors.Join(err, unlessTeardown(<-playback))
	case err := <-judged:
		cancel()
		return errors.Join(err, unlessTeardown(<-playback))
	case err := <-playback:
		cancel()
		var local error
		select {
		case local = <-delivered:
			c.ran = local == nil
		case local = <-judged:
		}
		return errors.Join(err, unlessTeardown(local))
	}
}

func unlessTeardown(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
