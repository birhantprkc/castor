package execute

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/engine/deliver"
	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/cast/policy/watch"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/subtitle"
)

func (c *cast) read(ctx context.Context) error {
	source, err := ffmpeg.NewProgramSource(c.attempt.Program, c.attempt.Read)
	if err != nil {
		return err
	}
	facts := measure(ctx, "the source this cast buffers", c.cfg.Probes.Source(c.attempt.Program, source.ProbeInputs()))

	if c.cfg.Subtitles != nil && (!facts.Measured || facts.Probe.AudioCodec != "") {
		c.burn = c.cfg.Subtitles(ctx, c.workDir)
	}

	floor, err := plan.Floor(ctx, plan.Inputs{
		Probe:     facts.Probe,
		Into:      ffmpeg.SpoolFormat,
		Decode:    c.attempt.Decode,
		MaxHeight: c.cfg.MaxHeight,
		Encoders:  c.cfg.Encoders,
	})
	if err != nil {
		return err
	}
	logRefusals(ctx, floor)

	reader, err := startPull(ctx, pullSpec{
		ffmpegPath: c.cfg.FFmpegPath,
		program:    c.attempt.Program,
		policy:     c.attempt.Read,
		source:     source,
		probe:      facts.Probe,
		spoolPath:  filepath.Join(c.workDir, "spool"+ffmpeg.SpoolFormat.Extension),
		floor:      floor,
		pcm:        c.burn != nil,
	})
	if err != nil {
		return err
	}
	c.reader, c.spool = reader, reader.spool

	c.evidence.Reached = attempt.PhaseReading
	return nil
}

func (c *cast) transcribe(ctx context.Context) error {
	if c.burn == nil {
		return nil
	}
	c.group.Go(func() error {
		c.burn.Run(ctx, c.reader.pcm)
		return nil
	})
	return nil
}

// readErr is the read's own terminal error, and only where the read has terminated (see pull.Err).
func (c *cast) readErr() error { return c.reader.Err() }

type pull struct {
	pcm io.ReadCloser

	ffmpegPath string
	source     ffmpeg.ProgramSource
	probe      media.ProbeInfo
	policy     read.Plan
	verbose    bool
	pcmOut     *io.PipeWriter

	floor plan.MediaPlan

	spool *deliver.Spool
	done  chan struct{}
	err   error

	proc *ffmpeg.Process
}

var _ watch.Producer = (*pull)(nil)

// pullSpec is everything one upstream read starts with.
type pullSpec struct {
	ffmpegPath string
	program    media.Program
	policy     read.Plan
	source     ffmpeg.ProgramSource
	probe      media.ProbeInfo
	spoolPath  string
	floor      plan.MediaPlan
	pcm        bool
}

func startPull(ctx context.Context, spec pullSpec) (*pull, error) {
	program, floor, wantPCM := spec.program, spec.floor, spec.pcm
	sp, err := deliver.NewSpool(spec.spoolPath)
	if err != nil {
		return nil, err
	}
	p := &pull{
		ffmpegPath: spec.ffmpegPath,
		source:     spec.source,
		probe:      spec.probe,
		policy:     spec.policy,
		verbose:    slog.Default().Enabled(ctx, slog.LevelDebug),
		floor:      floor,
		spool:      sp,
		done:       make(chan struct{}),
	}
	if wantPCM {
		r, w := io.Pipe()
		p.pcm, p.pcmOut = r, w
	}

	if err := p.start(ctx); err != nil {
		sp.CloseWrite(err)
		if p.pcmOut != nil {
			_ = p.pcmOut.CloseWithError(err)
		}
		return nil, err
	}

	produced := floor.Encoded()
	// NewProgramSource refused every program Validate rejects, so the primary is present.
	primary, _ := program.PrimaryInput()
	slog.InfoContext(ctx, "upstream pull started",
		"pcm", wantPCM,
		"reencode_video", produced.Video,
		"reencode_audio", produced.Audio,
		"source", primary.URL,
		"header_keys", programHeaderKeys(program),
	)

	go p.logProgress(ctx)
	go p.run(ctx)
	return p, nil
}

func programHeaderKeys(program media.Program) []string {
	keys := map[string]struct{}{}
	for _, input := range program.Inputs {
		for key := range input.Headers {
			keys[key] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(keys))
}

func (p *pull) start(ctx context.Context) error {
	opts := ffmpeg.PullOptions{
		Source:        p.source,
		Probe:         p.probe,
		Video:         p.floor.Video,
		Audio:         p.floor.Audio,
		Verbose:       p.verbose,
		PCM:           p.pcmOut != nil,
		PCMSampleRate: subtitle.SampleRate,
	}
	cmd, err := ffmpeg.PullArgs(opts)
	if err != nil {
		return fmt.Errorf("building the puller command line: %w", err)
	}

	var startOpts []ffmpeg.StartOption
	if p.pcmOut != nil {
		startOpts = append(startOpts, ffmpeg.WithPCM(p.pcmOut))
	}
	proc, err := ffmpeg.Start(ctx, p.ffmpegPath, cmd, startOpts...)
	if err != nil {
		return fmt.Errorf("starting puller ffmpeg: %w", err)
	}

	// Full invocation at debug so the pull can be reproduced by hand.
	slog.DebugContext(ctx, "puller ffmpeg command", "path", p.ffmpegPath, "args", cmd.Args)

	p.proc = proc
	return nil
}

func (p *pull) run(ctx context.Context) {
	defer close(p.done)

	err := p.copyInto(p.proc)

	proc := p.proc
	if err != nil && ctx.Err() == nil {
		proc.LogStderrTail(ctx, "puller ffmpeg stderr")
		err = fmt.Errorf("upstream pull: %w", err)
	} else if ctx.Err() != nil {
		err = ctx.Err()
	}
	p.err = err
	p.spool.CloseWrite(err)
	if p.pcmOut != nil {
		_ = p.pcmOut.CloseWithError(err)
	}

	if err == nil {
		slog.InfoContext(ctx, "upstream pull complete", "spooled_bytes", p.spool.Size())
	}
}

func (p *pull) copyInto(proc *ffmpeg.Process) error {
	_, copyErr := io.Copy(p.spool, proc.Stdout)
	return cmp.Or(copyErr, proc.Wait())
}

func (p *pull) logProgress(ctx context.Context) {
	const interval = 10 * time.Second
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var last int64
	lastAt := time.Now()
	for {
		select {
		case <-p.done:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			size := p.spool.Size()
			sample := p.Progress()
			now := time.Now()
			slog.InfoContext(ctx, "pull progress",
				"spooled_bytes", size,
				"rate_bytes_per_sec", bytesPerSecond(size-last, now.Sub(lastAt)),
				"media_position", sample.Position.Round(time.Second),
				"speed", float64(sample.Speed),
				"readrate", p.policy.Pace(),
			)
			last, lastAt = size, now
		}
	}
}

func bytesPerSecond(delta int64, elapsed time.Duration) int64 {
	if elapsed <= 0 {
		return 0
	}
	return int64(float64(delta) / elapsed.Seconds())
}

// Progress is the sample the process keeps of its own -progress feed (see ffmpeg.Process.Progress).
func (p *pull) Progress() media.Progress {
	if p.proc == nil {
		return media.Progress{}
	}
	return p.proc.Progress()
}

// Done is closed once the download has finished, cleanly or not, and the spool's write side with it.
func (p *pull) Done() <-chan struct{} { return p.done }

func (p *pull) Err() error {
	select {
	case <-p.done:
		return p.err
	default:
		return nil
	}
}

func (p *pull) Evidence() []string {
	proc := p.proc
	if proc == nil {
		return nil
	}
	return proc.Evidence().Lines
}

func (p *pull) ExitStatus() int {
	proc := p.proc
	if proc == nil {
		return -1
	}
	return proc.Evidence().ExitStatus
}

// Copying is the halves this read passed through untouched, which is what a recovery must stop asking for.
func (p *pull) Copying() media.Axes { return p.floor.Encoded().Copying() }

func (p *pull) judgedPace() float64 {
	if p.pcmOut != nil || p.floor.Encoded().Any() {
		return 0
	}
	return p.policy.Pace()
}
