package execute

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// feed is what a served cast's encoder reads: the read's buffer where there is one, else the program's source.
type feed struct {
	buffered *buffered
	program  media.Program
}

// spliced is whether the plan joins a seamed program itself; a buffer was made one stream by the floor.
func (f feed) spliced() bool { return f.buffered == nil && f.program.Seamed() }

// delivery is what serves a cast: the mechanism the renderer fetches from, and what writes the bytes it serves.
type delivery struct {
	sink   sink
	output producer
}

// serve encodes what f reads for dev, delivers it, and watches it play; it reports how far the delivery got.
func (s *session) serve(dev device.Device, ws workspace, f feed, burn Burn) (attempt.Phase, error) {
	opts, err := s.encode(dev, f, burn)
	if err != nil {
		return 0, err
	}
	d, err := s.produce(dev, ws, f, opts, burn)
	if err != nil {
		return 0, err
	}
	if err := awaitArtifact(s.ctx, d); err != nil {
		return attempt.PhaseOpening, err
	}
	if err := hand(s.ctx, dev, d.sink.URL(), opts.Format.ContentType, false); err != nil {
		return attempt.PhaseOpening, err
	}
	slog.InfoContext(s.ctx, "streaming to device, press Ctrl+C to stop")
	delivered, err := supervising(s.ctx, dev, d, f)
	if err != nil || !delivered {
		return attempt.PhasePlaying, err
	}
	return attempt.PhasePlaying, d.sink.Settled()
}

func (s *session) produce(dev device.Device, ws workspace, f feed, opts transcode.EncodeOptions, burn Burn) (delivery, error) {
	o := opening(dev, opts, ws.localIP)
	// A burn-in follows the encoder's progress, so only a cast without one can do without an encoder.
	if f.buffered != nil && burn == nil && opts.Verbatim() {
		return s.relay(o, f.buffered.reader)
	}

	var tail io.ReadCloser
	if f.buffered != nil {
		var err error
		if tail, err = f.buffered.reader.spool.TailAt(s.ctx, 0); err != nil {
			return delivery{}, err
		}
	}
	closeTail := func() {
		if tail != nil {
			_ = tail.Close()
		}
	}

	dir := filepath.Join(ws.dir, "delivery")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		closeTail()
		return delivery{}, fmt.Errorf("creating the delivery directory: %w", err)
	}

	var step func(media.Progress)
	if burn != nil {
		step = burn.Follow(s.ctx)
	}
	proc, err := s.startEncoder(opts, tail, step, dir)
	if err != nil {
		closeTail()
		return delivery{}, err
	}

	o.Dir, o.Out = dir, proc.Stdout
	sk, err := sinkFor(o, proc.Progress)
	// Killed first, since everything after needs it to have stopped writing; the tail before the wait, which joins its copy.
	s.releases.push(func() error {
		proc.Kill()
		closeTail()
		if sk != nil {
			_ = sk.Close()
		}
		return encoderResult(s.ctx, proc, proc.Wait())
	})
	if err != nil {
		return delivery{}, fmt.Errorf("starting the delivery: %w", err)
	}
	return delivery{sink: sk, output: encoderOutput{proc: proc, ended: sk.Drained()}}, nil
}

// relay serves the read's own spool, the read standing in for an encoder that would rewrite it unchanged.
func (s *session) relay(o deliver.Opening, reader *pull) (delivery, error) {
	sk, err := spoolSink(o, reader.spool, reader.Done(), reader.Progress)
	if err != nil {
		return delivery{}, fmt.Errorf("starting the delivery: %w", err)
	}
	s.releases.push(func() error {
		_ = sk.Close()
		// The read stands in for the encoder, so a read that failed fails the cast as that encoder would have.
		if s.ctx.Err() != nil {
			return nil
		}
		return reader.Err()
	})
	slog.InfoContext(s.ctx, "serving the read's buffer as it is, since the encode would change nothing")
	return delivery{sink: sk, output: reader}, nil
}

// opening is the terms every delivery of this cast opens on.
func opening(dev device.Device, opts transcode.EncodeOptions, localIP string) deliver.Opening {
	return deliver.Opening{
		Format:        opts.Format,
		LocalIP:       localIP,
		Headers:       dev.StreamHeaders(opts.Format.ContentType),
		IdleGrace:     idleGrace,
		WriteDeadline: writeDeadline,
	}
}

const (
	// idleGrace is how long a renderer that stopped asking is waited for before the delivery is done.
	idleGrace = 30 * time.Second

	// writeDeadline outlasts a stall verdict, so the watch judges a quiet renderer before a write gives up.
	writeDeadline = watch.StallWindow + idleGrace
)

// sinkFor opens the mechanism the format's delivery kind names, judged against what the encoder made.
func sinkFor(o deliver.Opening, made func() media.Progress) (sink, error) {
	switch o.Format.Delivery {
	case container.DeliverSegmented:
		srv, err := deliver.OpenSegments(o)
		if err != nil {
			return nil, err
		}
		return segmentedSink{Segments: srv, made: made}, nil
	case container.DeliverStream:
		srv, err := deliver.OpenStream(o)
		if err != nil {
			return nil, err
		}
		return streamedSink{Stream: srv, made: made}, nil
	default:
		return nil, fmt.Errorf("no delivery mechanism for kind %v", o.Format.Delivery)
	}
}

// spoolSink streams a spool another writes, whole once drained closes, judged against what that writer made.
func spoolSink(o deliver.Opening, sp *deliver.Spool, drained <-chan struct{}, made func() media.Progress) (sink, error) {
	srv, err := deliver.OpenSpooledStream(o, sp, drained)
	if err != nil {
		return nil, err
	}
	return streamedSink{Stream: srv, made: made}, nil
}

// streamedSink judges a progressive stream by the share of what was made that one client took.
type streamedSink struct {
	*deliver.Stream
	made func() media.Progress
}

func (s streamedSink) Audience() watch.Audience { return s }

func (s streamedSink) Buffered() time.Duration { return s.made().Position }

func (s streamedSink) Settled() error {
	handed, _ := s.Handed()
	return watch.Shortfall(handed, s.made())
}

// segmentedSink can only state whether anything was fetched: its window deletes behind the live edge.
type segmentedSink struct {
	*deliver.Segments
	made func() media.Progress
}

func (segmentedSink) Audience() watch.Audience { return nil }

func (s segmentedSink) Settled() error { return watch.NoneFetched(s.Served(), s.made()) }

// awaitArtifact waits for the artifact the renderer will fetch to exist.
func awaitArtifact(ctx context.Context, d delivery) error {
	artifact := d.sink.Artifact()
	return watch.Watch(ctx, watch.Monitor{
		Subject:  artifact.Subject,
		Window:   watch.Opening,
		Producer: d.output,
		Landed:   artifact.Landed,
		Grace:    artifact.Grace,
	})
}
