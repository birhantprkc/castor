package execute

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
	"github.com/stupside/castor/services/mediaserver/internal/cast/transcode"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// feed is what a served cast's encoder reads: the read's buffer where there is one, else the program's source.
type feed struct {
	buffered *buffered
	program  media.Program
}

// spliced is whether the plan joins a seamed program itself; a buffer was made one stream by the floor.
func (f feed) spliced() bool { return f.buffered == nil && f.program.Seamed() }

// delivery is what serves a cast: the mechanism the device fetches from, and what writes the bytes it serves.
type delivery struct {
	sink   sink
	output producer
}

// producer is what writes the served bytes, judged by the media it has made rather than bytes a muxer pads.
type producer interface {
	health.Producer
	health.Telemetry
}

// serve encodes what f reads for the device, delivers it, and watches it play; it reports how far the delivery got.
func (s *pipeline) serve(work string, f feed, burn Burn) (health.Phase, error) {
	opts, err := s.encode(f, burn)
	if err != nil {
		return 0, err
	}
	d, err := s.produce(work, f, opts, burn)
	if err != nil {
		return 0, err
	}
	if err := awaitArtifact(s.ctx, d); err != nil {
		return health.Opening, err
	}
	if err := hand(s.ctx, s.cast.Device, d.sink.URL(), opts.Format.ContentType, false); err != nil {
		return health.Opening, err
	}
	slog.InfoContext(s.ctx, "streaming to device")
	delivered, err := supervising(s.ctx, s.cast.Device, d, f)
	if err != nil || !delivered {
		if err == nil && starved(f, d) {
			return health.Playing, health.DeviceStarved("the playing cast")
		}
		return health.Playing, err
	}
	return health.Playing, d.sink.Settled()
}

func (s *pipeline) produce(work string, f feed, opts transcode.EncodeOptions, burn Burn) (delivery, error) {
	o := deliver.Opening{
		Format:        opts.Format,
		Listeners:     s.cast.Listeners,
		Headers:       s.cast.Device.Capabilities().ServedHeaders,
		IdleGrace:     idleGrace,
		WriteDeadline: writeDeadline,
	}
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

	dir := filepath.Join(work, "delivery")
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

	sk, err := sinkFor(s.ctx, o, dir, proc.Stdout, proc.Progress)
	// Killed first, since everything after needs it to have stopped writing; the tail before the wait, which joins its copy.
	s.releases.push(func() error {
		proc.Kill()
		closeTail()
		if sk != nil {
			_ = sk.Close()
		}
		return encoderResult(s.ctx, proc)
	})
	if err != nil {
		return delivery{}, fmt.Errorf("starting the delivery: %w", err)
	}
	return delivery{sink: sk, output: encoderOutput{proc: proc, ended: sk.Drained()}}, nil
}

// relay serves the read's own spool, the read standing in for an encoder that would rewrite it unchanged.
func (s *pipeline) relay(o deliver.Opening, reader *pull) (delivery, error) {
	sk, err := spoolSink(s.ctx, o, reader.spool, reader.Done(), reader.Progress)
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

// starved reports whether the device ended on an unfinished read after taking everything it had made.
func starved(f feed, d delivery) bool {
	if f.buffered == nil {
		return false
	}
	select {
	case <-f.buffered.reader.Done():
		return false
	default:
	}
	streamed, ok := d.sink.(streamedSink)
	if !ok {
		return false
	}
	sent, _ := streamed.Handed()
	made := streamed.made()
	return made.Bytes > 0 && sent >= made.Bytes
}

const (

	// idleGrace is how long a device that stopped asking is waited for before the delivery is done.
	idleGrace = 30 * time.Second

	// writeDeadline outlasts a stall verdict, so the watch judges a quiet device before a write gives up.
	writeDeadline = health.StallWindow + idleGrace
)
