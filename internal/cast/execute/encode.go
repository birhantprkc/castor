package execute

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/cast/plan"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

func (c *cast) encode(ctx context.Context, f feed) error {
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

	facts, input, err := f.input(ctx, read.Ceiling(into.Delivery == container.DeliverSegmented, burnIn != ""))
	if err != nil {
		return err
	}

	decided, err := c.decide(ctx, caps, into, facts, burnIn, f.spliced)
	if err != nil {
		return err
	}
	c.opts = transcode.EncodeOptions{
		Input:  input,
		Format: into,
		Probe:  facts.Probe,
		Video:  decided.Video,
		Audio:  decided.Audio,
	}
	return nil
}

func (c *cast) bufferInput(ctx context.Context, ceiling read.Pace) (facts, transcode.EncodeInput, error) {
	measured := measure(ctx, "the local buffer this encode reads", c.cfg.Probes.File(c.spool.Path()))
	return measured, transcode.FromPipe(transcode.SpoolFormat, ceiling), nil
}

func (c *cast) sourceInput(ctx context.Context, ceiling read.Pace) (facts, transcode.EncodeInput, error) {
	policies := c.attempt.Read.Encoding(c.program, ceiling)
	source, err := transcode.NewProgramSource(c.program, policies, c.cfg.Binary)
	if err != nil {
		return facts{}, transcode.EncodeInput{}, err
	}
	measured := measure(ctx, "the source this remux reads", c.cfg.Probes.Source(c.program, source.ProbeInputs()))
	if source, err = transcode.NewProgramSource(aligned(c.program, measured.Probe.InputStarts), policies, c.cfg.Binary); err != nil {
		return facts{}, transcode.EncodeInput{}, err
	}
	return measured, transcode.FromSource(source), nil
}

func (c *cast) decide(ctx context.Context, caps media.Capabilities, into container.FormatInfo, facts facts, burnIn string, spliced bool) (plan.MediaPlan, error) {
	decided, err := plan.PlanMedia(ctx, plan.Inputs{
		Caps: caps, Probe: facts.Probe, Measured: facts.Measured, Into: into, MaxHeight: c.cfg.MaxHeight,
		Spliced: spliced,
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

func (c *cast) startEncoder(ctx context.Context, step func(media.Progress), dir string) (*ffmpeg.Process, error) {
	cmd, err := transcode.EncodeArgs(c.opts)
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

func (o encoderOutput) Evidence() []string { return o.proc.Evidence().Lines }
