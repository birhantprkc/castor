package execute

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/engine/deliver"
	"github.com/stupside/castor/internal/cast/engine/deliver/segments"
	"github.com/stupside/castor/internal/cast/engine/deliver/stream"
	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/cast/policy/watch"
	"github.com/stupside/castor/internal/container"
	"github.com/stupside/castor/internal/media"
)

func (c *cast) encode(ctx context.Context) error {
	caps := c.dev.Capabilities()
	into, err := plan.ServedFormat(caps)
	if err != nil {
		return err
	}

	var burnIn string
	if c.burn != nil {
		if burnIn, err = c.burn.Inputs(); err != nil {
			return err
		}
	}

	ceiling := read.Ceiling(into.Delivery == container.DeliverSegmented, burnIn != "")
	var (
		facts facts
		input ffmpeg.EncodeInput
	)
	if c.row.Kind.Buffers() {
		facts = measure(ctx, "the local buffer this encode reads", c.cfg.Probes.File(c.spool.Path()))
		input = ffmpeg.FromPipe(ffmpeg.SpoolFormat, ceiling)
	} else {
		source, err := ffmpeg.NewProgramSource(c.attempt.Program, c.attempt.Read.Encoding(c.attempt.Program, ceiling))
		if err != nil {
			return err
		}
		facts = measure(ctx, "the source this remux reads", c.cfg.Probes.Source(c.attempt.Program, source.ProbeInputs()))
		input = ffmpeg.FromSource(source)
	}

	decided, err := c.decide(ctx, caps, into, facts, burnIn)
	if err != nil {
		return err
	}
	c.opts = ffmpeg.EncodeOptions{
		Input:  input,
		Format: into,
		Probe:  facts.Probe,
		Video:  decided.Video,
		Audio:  decided.Audio,
	}
	return nil
}

func (c *cast) decide(ctx context.Context, caps media.Capabilities, into container.FormatInfo, facts facts, burnIn string) (plan.MediaPlan, error) {
	decided, err := plan.PlanMedia(ctx, plan.Inputs{
		Caps: caps, Probe: facts.Probe, Measured: facts.Measured, Into: into, MaxHeight: c.cfg.MaxHeight,
		// Attempt's evidence: axis a reader already died copying not handed to second process to copy again.
		Decode:   c.attempt.Decode,
		BurnIn:   burnIn,
		Encoders: c.cfg.Encoders,
	})
	if err != nil {
		return plan.MediaPlan{}, fmt.Errorf("planning media: %w", err)
	}

	slog.InfoContext(ctx, "encode decision",
		"video_codec", decided.Video.Name(),
		"audio_codec", decided.Audio.Name(),
		"source_video_codec", string(facts.Probe.VideoCodec),
		"source_video_profile", facts.Probe.VideoProfile,
		"source_video_height", facts.Probe.VideoHeight,
		"source_audio_codec", string(facts.Probe.AudioCodec),
		"source_audio_channels", facts.Probe.AudioChannels,
		"measured", facts.Measured,
		"output_content_type", into.ContentType,
		"burn_in", burnIn != "",
		"decode", c.attempt.Decode.String(),
		"encoded", decided.Encoded().String(),
	)
	logRefusals(ctx, decided)
	return decided, nil
}

// logRefusals states once why each axis a plan encodes is not copied.
func logRefusals(ctx context.Context, decided plan.MediaPlan) {
	for _, r := range decided.Refusals {
		slog.InfoContext(ctx, "not copied", "reason", string(r.Reason), "why", r.Why)
	}
}

func (c *cast) produce(ctx context.Context) error {
	if c.row.Kind.Buffers() {
		tail, err := c.spool.Tail(ctx)
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

	sink, err := sinkFor(deliver.Opening{
		Format:        c.opts.Format,
		LocalIP:       c.localIP,
		Dir:           dir,
		Out:           proc.Stdout,
		Headers:       c.dev.StreamHeaders(c.opts.Format.ContentType),
		IdleGrace:     idleGrace,
		WriteDeadline: writeDeadline,
	}, proc.Progress)
	if err != nil {
		return fmt.Errorf("starting the delivery: %w", err)
	}
	c.sink = sink
	return nil
}

const (
	// idleGrace is how long a renderer that stopped asking is waited for before the delivery is done.
	idleGrace = 30 * time.Second

	// writeDeadline outlasts a stall verdict, so the watch judges a quiet renderer before a write gives up.
	writeDeadline = watch.StallWindow + idleGrace
)

// sinkFor opens the mechanism the format's delivery kind names, judged against what the encoder made.
func sinkFor(o deliver.Opening, made func() media.Progress) (Sink, error) {
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

func (c *cast) startEncoder(ctx context.Context, step func(media.Progress), dir string) (*ffmpeg.Process, error) {
	cmd, err := ffmpeg.EncodeArgs(c.opts)
	if err != nil {
		return nil, fmt.Errorf("building encode args: %w", err)
	}

	slog.DebugContext(ctx, "encoder ffmpeg command", "path", c.cfg.FFmpegPath, "args", cmd.Args)

	startOpts := []ffmpeg.StartOption{
		ffmpeg.WithWorkDir(dir),
		ffmpeg.WithProgress(step),
	}
	if c.tail != nil {
		startOpts = slices.Insert(startOpts, 0, ffmpeg.WithStdin(c.tail))
	}
	proc, err := ffmpeg.Start(ctx, c.cfg.FFmpegPath, cmd, startOpts...)
	if err != nil {
		return nil, fmt.Errorf("starting transcode: %w", err)
	}
	return proc, nil
}

func (c *cast) opened(ctx context.Context) error {
	c.evidence.Reached = attempt.PhaseOpening

	artifact := c.sink.Artifact()
	return watch.Watch(ctx, watch.Monitor{
		Subject:  artifact.Subject,
		Window:   watch.Opening,
		Producer: encoderOutput{proc: c.proc, ended: c.sink.Drained()},
		Landed:   artifact.Landed,
		Grace:    artifact.Grace,
	})
}

func (c *cast) settled(context.Context) error {
	if !c.ran {
		return nil
	}
	return c.sink.Settled()
}

func encoderResult(ctx context.Context, proc *ffmpeg.Process, waitErr error) error {
	err := cmp.Or(waitErr, proc.SilentFailure())
	if err == nil || ctx.Err() != nil || proc.Evidence().ExitStatus < 0 {
		return nil
	}
	proc.LogStderrTail(ctx, "ffmpeg stderr")
	return fmt.Errorf("encoder: %w", err)
}

type encoderOutput struct {
	proc  *ffmpeg.Process
	ended <-chan struct{}
}

func (o encoderOutput) Done() <-chan struct{} { return o.ended }
func (o encoderOutput) Evidence() []string    { return o.proc.Evidence().Lines }
