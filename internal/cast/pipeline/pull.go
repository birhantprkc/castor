package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/deliver/spool"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/subtitle/whisper"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// pull is the running upstream download. Exactly one pull touches the source
// URL per cast: it remuxes the stream into the spool (codec copy, cheap), paced
// like a buffering player, and, when requested, tees a PCM audio feed for the
// transcriber. Everything downstream reads local data, so the CDN sees a single
// well-behaved client and can never interrupt playback of what's already spooled.
//
// It decides nothing. Which axes the spool container cannot carry as they are was
// settled before it started, by the caller, from a probe of the source (see
// carriage.Known), and arrives here as the axes to re-encode. A download that made
// its own decisions would have to be able to take them back, which means rewinding
// a spool something may already be reading.
type pull struct {
	// pcm is the mono s16le audio feed, nil unless requested. The consumer must
	// keep draining it until EOF: backpressure on this pipe throttles the whole
	// download.
	pcm io.ReadCloser

	cfg     core.TranscodeConfig
	source  ffmpeg.NetworkSource
	verbose bool
	pcmOut  *io.PipeWriter

	// maxHeight is the height ceiling a floor encode of this read is held to, carried
	// because a read that PRODUCES a picture is bound by the same ceiling as the encode
	// downstream of the buffer it writes.
	maxHeight int

	// reencode is the axes this read was told to produce rather than pass through, kept
	// because its complement is what a failure of this read can be ABOUT: only a copied
	// axis can die on the bitstream filter ffmpeg inserts on its way into the buffer's
	// container, and only a copied axis is worth telling the next attempt to stop copying.
	reencode carriage.Axes

	spool *spool.Spool
	done  chan struct{}
	err   error

	// mu guards proc and progress, both of which are read while the pull runs: the
	// gate reads the stderr tail, and anything judging this read reads the sample.
	mu       sync.Mutex
	proc     *ffmpeg.Process
	progress media.Progress
}

// Compile-time proof that the read is observable on the terms a health rule reads: what
// has arrived, whether it is over, why it ended, and what it printed. The pull decides
// nothing about any of it, which is the property the assertion protects.
var _ watch.Producer = (*pull)(nil)

// startPull launches the upstream ffmpeg. Its mpegts output lands in sp; the
// optional PCM output is exposed as pull.pcm. It is device-blind: the read-once
// composition buffers every source this way regardless of renderer family, and only
// the wantPCM flag (driven by whether this cast runs a stage that consumes the
// samples) changes what it produces.
//
// policy is how the upstream is fetched, chosen from what the source published
// before anything started (see read.For). It is handed in rather than derived here
// for the same reason nothing else about this download is decided here.
func startPull(ctx context.Context, cfg core.TranscodeConfig, resolved *media.Stream, policy read.Policy, sp *spool.Spool, reencode carriage.Axes, maxHeight int, wantPCM bool) (*pull, error) {
	p := &pull{
		cfg:       cfg,
		source:    ffmpeg.NewNetworkSource(resolved, policy),
		verbose:   slog.Default().Enabled(ctx, slog.LevelDebug),
		reencode:  reencode,
		maxHeight: maxHeight,
		spool:     sp,
		done:      make(chan struct{}),
	}
	if wantPCM {
		r, w := io.Pipe()
		p.pcm, p.pcmOut = r, w
	}

	if err := p.start(ctx); err != nil {
		return nil, err
	}

	slog.InfoContext(ctx, "upstream pull started",
		"pcm", wantPCM,
		"reencode_video", reencode.Video,
		"reencode_audio", reencode.Audio,
		"source", resolved.URL.String(),
		"header_keys", slices.Sorted(maps.Keys(resolved.Headers)),
	)

	go p.logProgress(ctx)
	go p.run(ctx)
	return p, nil
}

// start launches the pull's ffmpeg and installs it as the current process. What it
// re-encodes rather than copies was decided before anything ran (see carriage.Known and
// attempt.Attempt.Decode) and is carried on the pull, so the process and the account of
// what it was doing cannot disagree.
func (p *pull) start(ctx context.Context) error {
	opts := ffmpeg.PullOptions{
		Source:        p.source,
		Reencode:      p.reencode,
		MaxHeight:     p.maxHeight,
		Verbose:       p.verbose,
		PCM:           p.pcmOut != nil,
		PCMSampleRate: whisper.SampleRate,
	}
	args := ffmpeg.PullArgs(opts)

	// How many extra pipes to open is the builder's answer, not a count kept here: it
	// is what routes the outputs onto them.
	proc, err := ffmpeg.Start(ctx, p.cfg.FFmpegPath, args, ffmpeg.WithExtraPipes(opts.ExtraPipes()))
	if err != nil {
		return fmt.Errorf("starting puller ffmpeg: %w", err)
	}

	// Full invocation at debug so the pull can be reproduced by hand
	// (ffmpeg <args>) to isolate source/network from the rest of the pipeline.
	slog.DebugContext(ctx, "puller ffmpeg command", "path", p.cfg.FFmpegPath, "args", args)

	p.mu.Lock()
	p.proc = proc
	p.mu.Unlock()

	go p.watch(proc)
	return nil
}

// watch keeps the latest sample the reader reports about itself. It runs for the life
// of the process whether or not anything reads Progress, because -progress is a
// blocking write: an unread feed stops the download once the pipe buffer fills.
//
// It observes and decides nothing, for the same reason the rest of this stage does
// not: a download that acted on its own judgement would have to be able to take the
// action back, which means rewinding a spool something may already be reading.
func (p *pull) watch(proc *ffmpeg.Process) {
	feed := proc.ProgressFeed()
	defer func() { _ = feed.Close() }()
	ffmpeg.WatchProgress(feed, func(sample media.Progress) {
		p.mu.Lock()
		p.progress = sample
		p.mu.Unlock()
	})
}

// current returns the running ffmpeg attempt.
func (p *pull) current() *ffmpeg.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.proc
}

// run drives the download to its terminal state and settles it. The spool's write
// side and the PCM pipe are always closed on return, so nothing downstream is
// left parked on a producer that has stopped.
func (p *pull) run(ctx context.Context) {
	defer close(p.done)

	err := p.copyInto(p.current())

	proc := p.current()
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

// copyInto moves the pull's remuxed output into the spool, and forwards the PCM
// tee to the transcriber alongside it when there is one.
func (p *pull) copyInto(proc *ffmpeg.Process) error {
	var wg sync.WaitGroup
	if pcm := proc.PCMFeed(); p.pcmOut != nil && pcm != nil {
		wg.Go(func() { _, _ = io.Copy(p.pcmOut, pcm) })
	}
	_, copyErr := io.Copy(p.spool, proc.Stdout)
	waitErr := proc.Wait()
	wg.Wait()
	return cmp.Or(copyErr, waitErr)
}

// logProgress reports the download at INFO every ten seconds: a rate of 0 makes a
// throttled or stalled CDN immediately visible instead of a silent hang. INFO and
// not DEBUG because a signed source URL that has expired is castor's single most
// common field failure and this line is its whole fingerprint, so a user has to be
// able to read it from an ordinary run rather than from one they knew in advance
// to launch with --debug.
//
// The byte rate is not enough on its own and never was. A read that delivered 14.4 MB
// and then nothing printed "spooled_bytes=14390648 rate_bytes_per_sec=0" twice in a
// row, which is also what a finished download prints, and 7.2 Mbit/s while it was
// moving looked healthy against every rate that source published (none). speed= says
// what those numbers cannot: media seconds per wall-clock second, against the pace
// the read was allowed.
func (p *pull) logProgress(ctx context.Context) {
	const interval = 10 * time.Second
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var last int64
	for {
		select {
		case <-p.done:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			size := p.spool.Size()
			sample := p.Progress()
			slog.InfoContext(ctx, "pull progress",
				"spooled_bytes", size,
				"rate_bytes_per_sec", (size-last)/int64(interval.Seconds()),
				"media_position", sample.Position.Round(time.Second),
				"speed", float64(sample.Speed),
				"readrate", p.source.Read.Pace.Realtime,
			)
			last = size
		}
	}
}

// Progress is the latest sample the reader reported about itself: how much media it
// has produced, how many bytes that was, and the speed it is arriving at. The zero
// value is what a read that has not yet muxed a packet honestly has to say, since
// ffmpeg answers N/A for every field until then.
func (p *pull) Progress() media.Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.progress
}

// Done is closed when the download has finished (cleanly or not) and the
// spool's write side is closed.
func (p *pull) Done() <-chan struct{} { return p.done }

// Err returns the terminal download error, if any. Only valid after Done.
func (p *pull) Err() error { return p.err }

// Evidence is what the puller ffmpeg has printed, retained even while the download is
// still running. It is what explains a stall: when a judgement gives up on this read,
// ffmpeg is killed by context cancellation, so its own error path (which would otherwise
// dump these lines) never runs. An empty tail means ffmpeg connected and emitted nothing;
// lines like "Server returned 403/404" mean the link is expired or blocked.
//
// It answers nothing at all before the process exists. An observation method is read from
// another goroutine at whatever instant a judgement is reached, so it has to be total over
// the read's whole life rather than only over the part of it that has a process.
func (p *pull) Evidence() []string {
	proc := p.current()
	if proc == nil {
		return nil
	}
	return proc.Evidence().Lines
}

// ExitStatus is the status this read's ffmpeg exited with, and it is the difference
// between a read that failed at something and a read castor stopped. It is negative
// while there is none to read: before the process exists, before it has been waited on,
// and forever after castor killed it, which is every fault castor names itself.
//
// A classification that read "no status" as an exit would blame the copy for every stall,
// so the sign is the contract here (see attempt.Evidence.ReadExit).
func (p *pull) ExitStatus() int {
	proc := p.current()
	if proc == nil {
		return -1
	}
	return proc.Evidence().ExitStatus
}

// Copying is the halves of the program this read was passing through untouched, which is
// what a failure of it can be about and what a recovery has to stop asking for.
func (p *pull) Copying() carriage.Axes { return p.reencode.Copying() }

// judgedPace is the pace this read may be held to, which is the whole of what arms any
// deliverability verdict about it (see watch.Health.Headroom). Zero withholds the question,
// and it is the honest answer wherever the LINK is not what decides how fast this read runs,
// because that verdict says the source cannot sustain the cast: before playback it answers
// that by walking every other admitted link, re-touching single-use signed URLs for a fault
// none of them caused, and in flight it ends a cast someone is watching with a message
// naming that link.
//
// Two things castor itself does inside this read take the answer away:
//
//   - a PCM tee. The feed is a pipe with one reader, whose consumer loads a whisper model
//     and runs inference on the goroutine draining it, and backpressure there throttles
//     the WHOLE download (see pull.pcm), so out_time freezes and the cumulative speed
//     crosses under playback rate a few seconds later. A larger model makes it worse, and
//     the model is a configured knob;
//   - a floor encode. Speed measures the reader's throughput end to end while the pace was
//     only ever an allowance on the network, so an axis this read PRODUCES rather than
//     copies is castor's own encoder being measured and reported as a starving source.
//
// It is ONE answer for the whole life of the read and not one per window, and both windows
// take it from here (see watchTheRead). The alternative was arming the in-flight arm with the
// pace the policy granted, on the argument that a producer under playback rate drains the
// renderer's buffer whoever is slow. That argument is true and still does not license the
// verdict: on a read castor throttles, the measurement identifies the wrong party, the
// message sends a user after their network, and the cast is not started over afterwards. A
// read of this shape that genuinely goes quiet is still named in flight, by the stall rule,
// which reads no pace at all and claims nothing about the link.
func (p *pull) judgedPace() float64 {
	if p.pcmOut != nil || p.reencode.Any() {
		return 0
	}
	return p.source.Read.Pace.Realtime
}
