package pipeline

import (
	"cmp"
	"context"
	"log/slog"
	"path/filepath"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/deliver/spool"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// readOnce is the served cast for a renderer that never fetches for itself: one reader into a
// local buffer (plus PCM into a transcription), the buffer tailed by an encoder (drawing cues
// when this cast burns them), served to the renderer.
//
// The renderer is awaited only after the buffer is playable, which is what lets its discovery
// overlap the read instead of ageing the signed source URL ahead of it. Nothing before that
// point needs it: what it declares fixes the container this cast is served in and its
// negotiated capabilities drive copy-vs-encode, and both come after the bytes.
func readOnce(ctx context.Context, c *cast) landing {
	// Nil for a cast that runs no stage, decided once by the party that owns the mechanism.
	stage := c.stage(ctx, c.cfg, c.workDir)
	sp, pl, err := c.startReading(ctx, stage)
	if err != nil {
		return landing{err: err}
	}

	// From here on the read is a party to whatever goes wrong, so every return reports its own
	// terminal state and its own stderr beside the cast's error. Both are needed here and
	// nowhere else: this is where an encoder can be blamed for a source that died, and the read
	// is the only party that can explain a stall, since castor kills it and its own error path
	// never runs.
	landed := func(reached attempt.Phase, err error) landing {
		return landing{
			reached:    reached,
			err:        err,
			readErr:    settled(pl),
			readLines:  pl.Evidence(),
			readExit:   pl.ExitStatus(),
			readCopied: pl.Copying(),
		}
	}

	// A nil lead tells the readiness rules that no transcription frontier is part of being
	// playable here, which a stage reporting zero would not: it would say "it has committed
	// nothing" forever.
	var lead watch.Lead
	if stage != nil {
		lead = stage.Lead()
	}
	if err := waitForPlayable(ctx, lead, sp, pl); err != nil {
		return landed(attempt.PhaseReading, err)
	}
	dev, err := c.renderer(ctx)
	if err != nil {
		return landed(attempt.PhaseReading, err)
	}
	// The phase the delivery answers with, never a literal: past its Play call this cast is one a
	// viewer is watching, and there are ordinary failures past it. A fragment that arrives
	// unresynchronisable kills the reader mid-title on "Invalid NAL unit size" at exit 183, and
	// that status over the axes it was copying is the whole of the broken-copy class. Reported as
	// reading, it earned a second attempt with a fresh work directory, a fresh connect and a fresh
	// Play, which is the film started over from the beginning at minute forty.
	reached, err := c.serveBuffer(ctx, dev, sp, pl, stage)
	if err != nil {
		return landed(reached, err)
	}
	return landed(attempt.PhaseDelivered, nil)
}

// startReading opens the local buffer and starts the one read that touches the source, with
// this cast's stage attached to the audio it tees.
//
// The order is the only one available: the read has to be told whether to tee audio before it
// starts, which is why the stage is built before it, and it has to be told what the buffer's
// container cannot carry, which is why the source is measured before it.
func (c *cast) startReading(ctx context.Context, stage Stage) (*spool.Spool, *pull, error) {
	sp, err := spool.New(filepath.Join(c.workDir, "spool"+ffmpeg.SpoolFormat.Extension))
	if err != nil {
		return nil, nil, err
	}

	facts := core.Measure(ctx, "the source this cast buffers",
		ffmpeg.SourceProbe(c.cfg.Resolver.FFprobePath, ffmpeg.NewNetworkSource(c.attempt.Source, c.attempt.Read)))

	// The tee is asked for exactly when something will drain it: the pipe is unbuffered, so a
	// feed nobody reads blocks the whole download.
	pl, err := startPull(ctx, c.cfg.Transcode, c.attempt.Source, c.attempt.Read, sp,
		bufferCarriage(ctx, facts, c.attempt.Decode), c.cfg.Resolver.MaxHeight, stage != nil)
	if err != nil {
		return nil, nil, err
	}
	if stage != nil {
		stage.Attach(ctx, c.group, pl.pcm)
	}
	return sp, pl, nil
}

// serveBuffer hands the renderer the encode that tails the buffer, and keeps judging the read
// behind it for as long as the renderer is playing: this is the composition where a starving
// link and a renderer that never fetches are both still possible after playback has started,
// and both used to run to the end of the title reported as success.
//
// It reports the phase with the error, and the two returns above the delivery state it for
// themselves: neither the encode this leg builds nor the reader it opens over the buffer has
// been anywhere near a renderer, so what they reach is the read and nothing further.
func (c *cast) serveBuffer(ctx context.Context, dev Renderer, sp *spool.Spool, pl *pull, stage Stage) (attempt.Phase, error) {
	opts, err := c.bufferedEncode(ctx, dev.Capabilities(), sp.Path(), stage)
	if err != nil {
		return attempt.PhaseReading, err
	}
	// Handed to the delivery, which owns it from here: reaping the encoder means closing what it
	// reads, and only the party that reaps can know when that is (see core.OpenParams.Input). A
	// defer here could not do the job, since it cannot run until the delivery has returned, which
	// is what it would be waiting for.
	tail, err := sp.Tail(ctx)
	if err != nil {
		return attempt.PhaseReading, err
	}

	// Nil where no stage follows the encoder, which is not a missing consumer: the delivery
	// driver drains that feed either way (ffmpeg's -progress write blocks, and an unread one
	// stops the encode dead), so nil only says that no cast pays for a step it has no use for.
	var follow func(media.Progress)
	if stage != nil {
		follow = stage.Follow(ctx)
	}
	return c.serve(ctx, dev, attempt.PhaseReading, core.OpenParams{
		Opts:       opts,
		Input:      tail,
		OnProgress: follow,
	}, func(ctx context.Context, d core.Delivery) error {
		return supervise(ctx, sp, pl, d)
	})
}

// bufferedEncode is the encode that tails the buffer: the container the renderer asked for out,
// read over stdin, height-capped, GOP-bounded and carrying whatever the stage needs drawn.
//
// The measurement is the BUFFER'S and not the source's, and that distinction is load-bearing
// rather than incidental. The MPEG-TS buffer re-frames everything that passes through it: an
// AAC track that arrived as fMP4 comes out the far side as ADTS, so this encode needs a repack
// that a direct remux of the same source does not, and a decision taken from the original
// source is a decision about a stream nobody is reading. A failed or partial measurement leaves
// nothing known, which no copy rule accepts, so it falls back to a re-encode.
func (c *cast) bufferedEncode(ctx context.Context, caps media.Renderer, buffer string, stage Stage) (ffmpeg.EncodeOptions, error) {
	into, err := core.ServedFormat(caps)
	if err != nil {
		return ffmpeg.EncodeOptions{}, err
	}
	// The cue file has to exist before ffmpeg starts or drawtext's filter init fails, which is
	// why the stage is asked for its inputs here and the answer becomes an input to the video
	// decision. That ordering used to be a contract held open by a comment: attach before the
	// resolver, or ship a cast that plays with no subtitles and no error anywhere.
	var burnIn string
	if stage != nil {
		if burnIn, err = stage.Inputs(); err != nil {
			return ffmpeg.EncodeOptions{}, err
		}
	}
	facts := core.Measure(ctx, "the local buffer this encode reads", ffmpeg.FileProbe(c.cfg.Resolver.FFprobePath, buffer))
	return c.encode(ctx, caps, into, encodeInput{facts: facts, pipe: ffmpeg.SpoolFormat, burnIn: burnIn}), nil
}

// bufferCarriage is what the read must produce rather than pass through: what the buffer's
// container cannot carry as it is, plus whatever a previous attempt of this cast proved
// cannot be copied at all.
//
// The first is worth a measurement of the source before the read opens it, a second touch of a
// URL the read is otherwise careful to touch once: MPEG-TS does not refuse a codec it has no
// stream type for, it writes the track as unreadable private data and exits cleanly, so without
// this the cast silently loses a track for the whole title. What was never measured answers
// "nothing known against it" and the copy is attempted, which is the same answer castor gives
// any source it could not measure, and it needs no clause of its own to say so: an unmeasured
// codec matches no carriage rule.
//
// The second cannot be measured at all, which is why it arrives as a term of the attempt. A
// truncated fragment desynchronises the bitstream filter this copy carries into MPEG-TS and
// kills the reader on "Invalid NAL unit size", and nothing about the source's codecs predicts
// it: the same packets copy cleanly when they arrive whole. Only having watched a reader die
// on them is evidence, and that evidence belongs to the cast rather than to this leg.
func bufferCarriage(ctx context.Context, facts core.Facts, decode carriage.Axes) carriage.Axes {
	refused := carriage.Known(facts.Probe, ffmpeg.SpoolFormat)
	if refused.Any() {
		why, whyAudio := carriage.Reason(facts.Probe, ffmpeg.SpoolFormat)
		slog.InfoContext(ctx, "the buffer's container will not carry this source as it is; re-encoding into it",
			"video", refused.Video, "audio", refused.Audio, "reason", cmp.Or(why, whyAudio))
	}
	if decode.Any() {
		slog.InfoContext(ctx, "a previous attempt's copy broke upstream; this read decodes it into the buffer instead",
			"axes", decode.String())
	}
	return refused.Or(decode)
}

// settled is the read's own terminal error, read only where the read has actually terminated:
// the reader publishes it with the close of its done channel and not before, so asking earlier
// reads a field the download is still writing. A read still running has no terminal error to
// report, which is exactly what nil says.
func settled(pl *pull) error {
	select {
	case <-pl.Done():
		return pl.Err()
	default:
		return nil
	}
}
