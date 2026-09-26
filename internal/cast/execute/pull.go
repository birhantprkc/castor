package execute

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/plan"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

type pull struct {
	pcm io.ReadCloser

	ffmpegPath string
	source     transcode.ProgramSource
	probe      media.ProbeInfo
	policy     read.Plan
	verbose    bool
	pcmOut     *io.PipeWriter
	pcmRate    int

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
	source     transcode.ProgramSource
	probe      media.ProbeInfo
	spoolPath  string
	floor      plan.MediaPlan
	// pcmRate is the rate of the PCM tee a transcription reads, zero for none.
	pcmRate int
}

func startPull(ctx context.Context, spec pullSpec) (*pull, error) {
	program, floor, wantPCM := spec.program, spec.floor, spec.pcmRate > 0
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
		pcmRate:    spec.pcmRate,
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
	opts := transcode.PullOptions{
		Source:        p.source,
		Probe:         p.probe,
		Video:         p.floor.Video,
		Audio:         p.floor.Audio,
		Verbose:       p.verbose,
		PCM:           p.pcmOut != nil,
		PCMSampleRate: p.pcmRate,
	}
	cmd, err := transcode.PullArgs(opts)
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
	// A clean exit is not a complete read: the demuxer skips or truncates media and still exits 0.
	return cmp.Or(copyErr, proc.Wait(), proc.SilentFailure())
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

// LostMedia reports a read that ended short of what its source declared.
func (p *pull) LostMedia() bool {
	proc := p.proc
	return proc != nil && proc.LostMedia()
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
