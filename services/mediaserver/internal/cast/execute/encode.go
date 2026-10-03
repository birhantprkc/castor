package execute

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/stupside/castor/services/mediaserver/internal/cast/container"
	"github.com/stupside/castor/services/mediaserver/internal/cast/fetch"
	"github.com/stupside/castor/services/mediaserver/internal/cast/plan"
	"github.com/stupside/castor/services/mediaserver/internal/cast/transcode"
	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// encode decides what the served encode does with what f reads, for the device and any burn-in.
func (s *pipeline) encode(f feed, burn Burn) (transcode.EncodeOptions, error) {
	caps := s.cfg.Device.Capabilities()
	into, ok := container.FormatForContentType(caps.ServedContainer)
	if !ok {
		return transcode.EncodeOptions{}, fmt.Errorf("the device asks to be served %q, which castor cannot produce", caps.ServedContainer)
	}

	var burnIn string
	if burn != nil {
		var err error
		if burnIn, err = burn.Inputs(); err != nil {
			return transcode.EncodeOptions{}, err
		}
	}

	facts, input, err := s.input(f, fetch.Ceiling(into.Delivery, burnIn != ""))
	if err != nil {
		return transcode.EncodeOptions{}, err
	}

	decided, err := s.decide(caps, into, facts, burnIn, f.spliced())
	if err != nil {
		return transcode.EncodeOptions{}, err
	}
	return transcode.EncodeOptions{
		Input:  input,
		Format: into,
		Probe:  facts.probe,
		Video:  decided.Video,
		Audio:  decided.Audio,
	}, nil
}

// input is what the encoder reads from f, measured where it reads it: the spool as it grows, or the source itself.
func (s *pipeline) input(f feed, ceiling fetch.Pace) (facts, transcode.EncodeInput, error) {
	if f.buffered != nil {
		measured := measure(s.ctx, "the local buffer this encode reads", s.cfg.Probes.File(f.buffered.reader.spool.Path()))
		return measured, transcode.FromPipe(ceiling), nil
	}
	policies := s.attempt.Fetch.Encoding(f.program, ceiling)
	source, err := transcode.NewProgramSource(f.program, policies, s.cfg.Binary, s.cfg.InputArgs)
	if err != nil {
		return facts{}, transcode.EncodeInput{}, err
	}
	measured := measure(s.ctx, "the source this remux reads", s.cfg.Probes.Source(f.program, source.ProbeInputs()))
	if source, err = transcode.NewProgramSource(aligned(f.program, measured.probe.InputStarts), policies, s.cfg.Binary, s.cfg.InputArgs); err != nil {
		return facts{}, transcode.EncodeInput{}, err
	}
	return measured, transcode.FromSource(source), nil
}

func (s *pipeline) decide(caps media.Capabilities, into container.FormatInfo, facts facts, burnIn string, spliced bool) (plan.Plan, error) {
	decided, err := plan.Served(s.ctx, plan.Inputs{
		Caps: caps, Probe: facts.probe, Measured: facts.measured, Into: into, MaxHeight: s.cfg.MaxHeight,
		Spliced: spliced,
		// Attempt's evidence: axis a reader already died copying not handed to second process to copy again.
		Decode:   s.attempt.Decode,
		BurnIn:   burnIn,
		Encoders: s.cfg.Encoders,
	})
	if err != nil {
		return plan.Plan{}, fmt.Errorf("planning media: %w", err)
	}

	slog.InfoContext(s.ctx, "encode decision",
		"video_codec", decided.Video.Name(),
		"audio_codec", decided.Audio.Name(),
		"source_video_codec", string(facts.probe.VideoCodec),
		"source_video_profile", facts.probe.VideoProfile,
		"source_video_height", facts.probe.VideoHeight,
		"source_audio_codec", string(facts.probe.AudioCodec),
		"source_audio_channels", facts.probe.AudioChannels,
		"measured", facts.measured,
		"output_content_type", into.ContentType,
		"burn_in", burnIn != "",
		"decode", s.attempt.Decode.String(),
		"encoded", decided.Encoded().String(),
	)
	logRefusals(s.ctx, decided)
	return decided, nil
}

// logRefusals states once why each axis a plan encodes is not copied.
func logRefusals(ctx context.Context, decided plan.Plan) {
	for _, r := range decided.Refusals {
		slog.InfoContext(ctx, "not copied", "reason", string(r.Reason), "why", r.Why)
	}
}

// startEncoder runs opts in dir, reading tail where it reads a pipe (nil where it reads the source).
func (s *pipeline) startEncoder(opts transcode.EncodeOptions, tail io.Reader, step func(media.Progress), dir string) (*ffmpeg.Process, error) {
	cmd, err := transcode.EncodeArgs(opts)
	if err != nil {
		return nil, fmt.Errorf("building encode args: %w", err)
	}

	slog.DebugContext(s.ctx, "encoder ffmpeg command", "path", s.cfg.FFmpegPath, "args", cmd.Args)

	proc, err := ffmpeg.Start(s.ctx, s.cfg.FFmpegPath, cmd, ffmpeg.Options{Stdin: tail, WorkDir: dir, Progress: step})
	if err != nil {
		return nil, fmt.Errorf("starting transcode: %w", err)
	}
	return proc, nil
}

func encoderResult(ctx context.Context, proc *ffmpeg.Process) error {
	err := cmp.Or(proc.Wait(), proc.SilentFailure())
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

func (o encoderOutput) Progress() media.Progress { return o.proc.Progress() }

// Err is nil: the encoder's own failure is read once, when it is released (see encoderResult).
func (encoderOutput) Err() error { return nil }
