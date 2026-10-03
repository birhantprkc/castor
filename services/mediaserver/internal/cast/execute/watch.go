package execute

import (
	"context"
	"errors"

	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
)

// gate holds a buffered cast until its read has proven it can deliver, and its transcription leads.
func gate(ctx context.Context, buf *buffered) error {
	return health.Watch(ctx, readMonitor(buf.reader, health.Monitor{
		Subject: "playback gate",
		Phase:   health.Reading,
		Lead:    buf.burn,
	}))
}

// awaitArtifact waits for the artifact the device will fetch to exist.
func awaitArtifact(ctx context.Context, d delivery) error {
	artifact := d.sink.Artifact()
	return health.Watch(ctx, health.Monitor{
		Subject:  artifact.Subject,
		Phase:    health.Opening,
		Producer: d.output,
		Landed:   artifact.Landed,
		Grace:    artifact.Grace,
	})
}

// supervising watches a served cast until its delivery, device or verdict ends it, reporting whether the delivery ran its course.
func supervising(ctx context.Context, dev Device, d delivery, f feed) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	delivered := make(chan error, 1)
	go func() { delivered <- d.sink.Wait(ctx) }()
	playback := make(chan error, 1)
	go func() { playback <- dev.AwaitEnd(ctx) }()

	// Nil where delivery has no supervisor; sink deleting behind its live edge reads as stall on every cast.
	var judged chan error
	if aud := d.sink.Audience(); aud != nil {
		judged = make(chan error, 1)
		go func() { judged <- health.Watch(ctx, playingMonitor(f, d, aud)) }()
	}

	select {
	case err := <-delivered:
		cancel()
		return err == nil, errors.Join(err, unlessTeardown(<-playback))
	case err := <-judged:
		cancel()
		return false, errors.Join(err, unlessTeardown(<-playback))
	case err := <-playback:
		cancel()
		select {
		case local := <-delivered:
			return local == nil, errors.Join(err, unlessTeardown(local))
		case local := <-judged:
			return false, errors.Join(err, unlessTeardown(local))
		}
	}
}

func unlessTeardown(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// readMonitor judges a buffered cast by its read.
func readMonitor(reader *pull, m health.Monitor) health.Monitor {
	m.Producer = reader
	m.Telemetry = reader
	m.Landed = reader.spool.Size
	return m
}

// playingMonitor judges a buffered cast by its read, and one reading its source by the encoder serving it.
func playingMonitor(f feed, d delivery, aud health.Audience) health.Monitor {
	m := health.Monitor{Subject: "the playing cast", Phase: health.Playing, Audience: aud}
	if f.buffered != nil {
		return readMonitor(f.buffered.reader, m)
	}
	m.Producer, m.Telemetry = d.output, d.output
	m.Landed = d.sink.Artifact().Landed
	return m
}
