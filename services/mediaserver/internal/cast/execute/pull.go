package execute

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/codec"
	"github.com/stupside/castor/services/mediaserver/internal/cast/deliver"
	"github.com/stupside/castor/services/mediaserver/internal/cast/fetch"
	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
	"github.com/stupside/castor/services/mediaserver/internal/cast/transcode"
	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

type pull struct {
	// pcm is the transcription's feed and pcmOut its write end, both nil where the read tees none.
	pcm    io.ReadCloser
	pcmOut *io.PipeWriter

	pace  float64
	floor codec.Plan
	spool *deliver.Spool
	proc  *ffmpeg.Process

	// done is closed once the read has ended, publishing err with it.
	done chan struct{}
	err  error
}

var _ health.Producer = (*pull)(nil)

// pullSpec is everything one upstream read starts with.
type pullSpec struct {
	ffmpegPath string
	program    media.Program
	policy     fetch.Plan
	source     transcode.ProgramSource
	probe      media.ProbeInfo
	spoolPath  string
	floor      codec.Plan
	// pcmRate is the rate of the PCM tee a transcription reads, zero for none.
	pcmRate int
}

func startPull(ctx context.Context, spec pullSpec) (*pull, error) {
	sp, err := deliver.NewSpool(spec.spoolPath)
	if err != nil {
		return nil, err
	}
	var (
		pcm    io.ReadCloser
		pcmOut *io.PipeWriter
	)
	if spec.pcmRate > 0 {
		r, w := io.Pipe()
		pcm, pcmOut = r, w
	}
	proc, err := startPuller(ctx, spec, pcmOut)
	if err != nil {
		sp.CloseWrite(err)
		if pcmOut != nil {
			_ = pcmOut.CloseWithError(err)
		}
		return nil, err
	}
	p := &pull{pcm: pcm, pcmOut: pcmOut, pace: spec.policy.Pace(), floor: spec.floor, spool: sp, proc: proc, done: make(chan struct{})}

	produced := spec.floor.Encoded()
	// NewProgramSource refused every program Validate rejects, so the primary is present.
	primary, _ := spec.program.PrimaryInput()
	slog.InfoContext(ctx, "upstream pull started",
		"pcm", pcmOut != nil,
		"reencode_video", produced.Video,
		"reencode_audio", produced.Audio,
		"source", primary.URL,
		"header_keys", spec.program.HeaderKeys(),
	)

	go p.logProgress(ctx)
	go p.run(ctx)
	return p, nil
}

// startPuller runs the read's ffmpeg, teeing PCM into pcmOut where there is one.
func startPuller(ctx context.Context, spec pullSpec, pcmOut *io.PipeWriter) (*ffmpeg.Process, error) {
	cmd, err := transcode.PullArgs(transcode.PullOptions{
		Source:        spec.source,
		Probe:         spec.probe,
		Video:         spec.floor.Video,
		Audio:         spec.floor.Audio,
		Verbose:       slog.Default().Enabled(ctx, slog.LevelDebug),
		PCMSampleRate: spec.pcmRate,
	})
	if err != nil {
		return nil, fmt.Errorf("building the puller command line: %w", err)
	}

	var opts ffmpeg.Options
	// Set only when there is a tee: a nil *PipeWriter in the field would read as a consumer.
	if pcmOut != nil {
		opts.PCM = pcmOut
	}
	proc, err := ffmpeg.Start(ctx, spec.ffmpegPath, cmd, opts)
	if err != nil {
		return nil, fmt.Errorf("starting puller ffmpeg: %w", err)
	}

	// Full invocation at debug so the pull can be reproduced by hand.
	slog.DebugContext(ctx, "puller ffmpeg command", "path", spec.ffmpegPath, "args", cmd.Args)
	return proc, nil
}

func (p *pull) run(ctx context.Context) {
	defer close(p.done)

	_, err := io.Copy(p.spool, p.proc.Stdout)
	// A clean exit is not a complete read: the demuxer skips or truncates media and still exits 0.
	err = cmp.Or(err, p.proc.Wait(), p.proc.SilentFailure())

	if err != nil && ctx.Err() == nil {
		p.proc.LogStderrTail(ctx, "puller ffmpeg stderr")
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
				"readrate", p.pace,
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
func (p *pull) Progress() media.Progress { return p.proc.Progress() }

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

func (p *pull) Evidence() []string { return p.proc.Evidence().Lines }

// lostMedia reports a read that ended short of what its source declared.
func (p *pull) lostMedia() bool { return p.proc.LostMedia() }

func (p *pull) exitStatus() int { return p.proc.Evidence().ExitStatus }

// copying is the halves this read passed through untouched, which is what a recovery must stop asking for.
func (p *pull) copying() media.Axes { return p.floor.Encoded().Copying() }

func (p *pull) judgedPace() float64 {
	if p.pcmOut != nil || p.floor.Encoded().Any() {
		return 0
	}
	return p.pace
}
