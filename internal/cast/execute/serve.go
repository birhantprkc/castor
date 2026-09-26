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
	"github.com/stupside/castor/internal/cast/deliver/segments"
	"github.com/stupside/castor/internal/cast/deliver/stream"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// feed is what a served cast's encoder reads: the local buffer, or the source itself.
type feed struct {
	input func(ctx context.Context, ceiling read.Pace) (facts, transcode.EncodeInput, error)
	// spliced is whether the plan joins a seamed program itself; a buffer was made one stream by the floor.
	spliced bool
	// stdin is nil where the encoder reads no pipe.
	stdin   func(ctx context.Context) (io.ReadCloser, error)
	playing func(watch.Monitor) watch.Monitor
}

func (c *cast) fromBuffer() feed {
	return feed{input: c.bufferInput, stdin: c.spool.Tail, playing: c.readMonitor}
}

func (c *cast) fromSource() feed {
	return feed{input: c.sourceInput, spliced: c.program.Seamed(), playing: c.encoderMonitor}
}

func (c *cast) serve(ctx context.Context, f feed) error {
	if err := c.encode(ctx, f); err != nil {
		return err
	}
	if err := c.produce(ctx, f); err != nil {
		return err
	}
	if err := c.opened(ctx); err != nil {
		return err
	}
	if err := c.hand(ctx, c.sink.URL(), c.opts.Format.ContentType); err != nil {
		return err
	}
	slog.InfoContext(ctx, "streaming to device, press Ctrl+C to stop")
	ran, err := c.supervising(ctx, f)
	if err != nil || !ran {
		return err
	}
	return c.sink.Settled()
}

func (c *cast) produce(ctx context.Context, f feed) error {
	// A burn-in follows the encoder's progress, so only a cast without one can do without an encoder.
	if c.burn == nil && c.opts.Verbatim() {
		return c.relay(ctx)
	}
	if f.stdin != nil {
		tail, err := f.stdin(ctx)
		if err != nil {
			return err
		}
		c.tail = tail
	}

	dir := filepath.Join(c.workDir, "delivery")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating the delivery directory: %w", err)
	}

	var step func(media.Progress)
	if c.burn != nil {
		step = c.burn.Follow(ctx)
	}
	proc, err := c.startEncoder(ctx, step, dir)
	if err != nil {
		return err
	}
	c.proc = proc

	opening := c.opening()
	opening.Dir, opening.Out = dir, proc.Stdout
	sink, err := sinkFor(opening, proc.Progress)
	if err != nil {
		return fmt.Errorf("starting the delivery: %w", err)
	}
	c.sink, c.output = sink, encoderOutput{proc: proc, ended: sink.Drained()}
	return nil
}

// relay serves the read's own spool, the read standing in for an encoder that would rewrite it unchanged.
func (c *cast) relay(ctx context.Context) error {
	sink, err := spoolSink(c.opening(), c.spool, c.reader.Done(), c.reader.Progress)
	if err != nil {
		return fmt.Errorf("starting the delivery: %w", err)
	}
	c.sink, c.output = sink, c.reader
	slog.InfoContext(ctx, "serving the read's buffer as it is, since the encode would change nothing")
	return nil
}

// opening is the terms every delivery of this cast opens on.
func (c *cast) opening() deliver.Opening {
	return deliver.Opening{
		Format:        c.opts.Format,
		LocalIP:       c.localIP,
		Headers:       c.dev.StreamHeaders(c.opts.Format.ContentType),
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
		srv, err := segments.Open(o)
		if err != nil {
			return nil, err
		}
		return segmentedSink{Server: srv, made: made}, nil
	case container.DeliverStream:
		srv, err := stream.Open(o)
		if err != nil {
			return nil, err
		}
		return streamedSink{Server: srv, made: made}, nil
	default:
		return nil, fmt.Errorf("no delivery mechanism for kind %v", o.Format.Delivery)
	}
}

// spoolSink streams a spool another writes, whole once drained closes, judged against what that writer made.
func spoolSink(o deliver.Opening, sp *deliver.Spool, drained <-chan struct{}, made func() media.Progress) (sink, error) {
	srv, err := stream.OpenSpool(o, sp, drained)
	if err != nil {
		return nil, err
	}
	return streamedSink{Server: srv, made: made}, nil
}

// streamedSink judges a progressive stream by the share of what was made that one client took.
type streamedSink struct {
	*stream.Server
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
	*segments.Server
	made func() media.Progress
}

func (segmentedSink) Audience() watch.Audience { return nil }

func (s segmentedSink) Settled() error { return watch.NoneFetched(s.Served(), s.made()) }

func (c *cast) opened(ctx context.Context) error {
	c.evidence.Reached = attempt.PhaseOpening

	artifact := c.sink.Artifact()
	return watch.Watch(ctx, watch.Monitor{
		Subject:  artifact.Subject,
		Window:   watch.Opening,
		Producer: c.output,
		Landed:   artifact.Landed,
		Grace:    artifact.Grace,
	})
}
