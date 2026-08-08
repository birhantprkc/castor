package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/subtitle"
	"github.com/stupside/castor/internal/cast/subtitle/cue"
	"github.com/stupside/castor/internal/cast/subtitle/whisper"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

const (
	// cueLeadBias compensates for the encoder pipeline running ahead of the
	// mux position that -progress reports: frames pass through drawtext
	// roughly an encoder-lookahead before they are muxed, so we look up the
	// cue slightly ahead of out_time. Whisper timing itself is only ~±0.5s
	// accurate, so this doesn't need to be exact.
	cueLeadBias = 1.0

	// cueWrapColumns is where subtitle lines wrap. ~42 columns matches
	// broadcast subtitle conventions and keeps two lines inside the safe
	// area at the drawtext font size.
	cueWrapColumns = 42
)

// stages is the optional work one cast runs beside its read, as a list. Whether this cast
// burns subtitles is decided once, when the list is built, so no site downstream re-reads it
// off a nil pointer, and a caption sidecar is another element rather than a second nil-shaped
// axis.
type stages []Stage

// burnInStages is the list a read-once cast runs: a whisper burn-in when the operator asked
// for one and whisper started, nothing at all otherwise.
//
// Both ways of ending up with nothing are decided here and only here. Whisper failing to
// initialise downgrades to a subtitle-less cast rather than blocking playback, which is the
// posture this stage has always had, and its cost used to be that the downgrade had to be
// re-read as a nil check at every site that touched the stage.
func burnInStages(ctx context.Context, cfg core.Config, workDir string) stages {
	if core.SubtitleForServed(cfg) != core.SubtitleBurnIn {
		return nil
	}
	s := newBurnIn(ctx, cfg.Whisper, workDir)
	if s == nil {
		return nil
	}
	return stages{s}
}

// wantPCM reports whether the read must tee an audio feed. It is the presence of a stage and
// not a second question: every stage there is today reads the samples, and one that did not
// would simply ignore a feed it was handed.
func (ss stages) wantPCM() bool { return len(ss) > 0 }

// attach starts every stage over the read's audio feed.
//
// The feed is a pipe and so has exactly one reader: two stages that both want the samples
// need a second tee out of the read, which is the read's decision to make and not this list's.
func (ss stages) attach(ctx context.Context, g *errgroup.Group, pcm io.ReadCloser) {
	for _, s := range ss {
		s.Attach(ctx, g, pcm)
	}
}

// inputs is what the encode must carry for these stages to work: the live text file to draw,
// empty when nothing draws. The files exist by the time this returns, which is what the video
// decision needs of them.
func (ss stages) inputs() (string, error) {
	var burnIn string
	for _, s := range ss {
		in, err := s.Inputs()
		if err != nil {
			return "", err
		}
		burnIn = cmp.Or(burnIn, in)
	}
	return burnIn, nil
}

// follow is the step the delivery driver takes per sample the encoder reports about itself,
// fanned out to every stage.
//
// Nil when nothing follows, and nil is not a missing consumer: the driver drains that feed
// either way, because ffmpeg writes it with a blocking write and an unread one stops the
// encode dead. What nil says is that no cast without a stage pays for one.
func (ss stages) follow(ctx context.Context) func(media.Progress) {
	if len(ss) == 0 {
		return nil
	}
	steps := make([]func(media.Progress), len(ss))
	for i, s := range ss {
		steps[i] = s.Follow(ctx)
	}
	return func(sample media.Progress) {
		for _, step := range steps {
			step(sample)
		}
	}
}

// lead is the frontier the readiness rule waits on, nil when no stage has one.
//
// Nil is the answer, deliberately, and not a stage reporting zero: it is how the readiness
// rules ask whether a transcription lead is part of being playable at all. A typed nil would
// answer "yes, and it has committed nothing", forever.
func (ss stages) lead() watch.Lead {
	for _, s := range ss {
		if l := s.Lead(); l != nil {
			return l
		}
	}
	return nil
}

// burnIn is the transcription stage: an in-process whisper model fed by the read's PCM tee,
// whose cues are drawn into the video by the encoder's drawtext filter via a live-swapped
// textfile. It is the mechanism behind the SubtitleBurnIn axis; SubtitleOff never constructs
// one.
type burnIn struct {
	tr      *whisper.Transcriber
	builder *cue.Builder
	path    string
}

// Compile-time proof that a burn-in is nothing more than one stage of a cast.
var _ Stage = (*burnIn)(nil)

// newBurnIn prepares the transcription stage, returning nil when whisper cannot start. That
// downgrades to a subtitle-less cast rather than blocking playback: the cast proceeds without
// burn-in instead of failing outright.
func newBurnIn(ctx context.Context, whisperCfg subtitle.Whisper, workDir string) *burnIn {
	tr, err := whisper.New(ctx, whisperCfg)
	if err != nil {
		slog.WarnContext(ctx, "whisper init failed; casting without subtitles", "error", err)
		return nil
	}
	return &burnIn{
		tr:      tr,
		builder: cue.NewBuilder(),
		path:    filepath.Join(workDir, "cue.txt"),
	}
}

// Attach consumes the PCM feed in g until EOF. If transcription fails, the feed keeps
// draining: backpressure on that pipe would otherwise stall the read and starve the buffer the
// encoder is playing from.
func (b *burnIn) Attach(ctx context.Context, g *errgroup.Group, pcm io.ReadCloser) {
	g.Go(func() error {
		defer pcm.Close()
		if err := b.tr.Run(ctx, pcm, b.builder); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "transcription failed; subtitles stop here", "error", err)
			_, _ = io.Copy(io.Discard, pcm)
		}
		return nil
	})
}

// Inputs creates the live cue file and returns its path, which is what the video decision
// takes as its burn-in input.
//
// Creating it is the point: drawtext re-opens the path before every frame and ffmpeg dies on a
// read that fails, so the file has to exist before the encoder starts. Handing back a path
// instead of writing into an encode is what removes the ordering hazard this stage used to
// carry, where a burn-in set after the copy-vs-encode decision produced a cast that played
// with no subtitles and no error anywhere.
func (b *burnIn) Inputs() (string, error) {
	if err := os.WriteFile(b.path, nil, 0o644); err != nil {
		return "", fmt.Errorf("creating subtitle cue file: %w", err)
	}
	return b.path, nil
}

// Follow returns the cue placement step: one call per sample the encoder reports about itself,
// keeping the textfile holding the line for the frame currently being encoded. The writer
// reads cues from the builder and transcription progress through frontier.
//
// The delivery driver calls this after reaping the encoder and before its caller removes the
// work directory, so no swap can land in a deleted directory.
func (b *burnIn) Follow(ctx context.Context) func(media.Progress) {
	return cueWriter(ctx, b.path, b.builder, b.tr.LatestEnd)
}

// Lead is how far transcription has committed, which is what the readiness rule holds a cast
// short of: a burn-in whose cues have not reached the frame being encoded ships a picture with
// nothing drawn on it.
func (b *burnIn) Lead() watch.Lead { return b.tr }

// cueWriter keeps the cue file holding the subtitle line for the frame currently
// being encoded. Updates are written to a temp file in the same directory and renamed
// into place: drawtext re-opens the path before every frame and a partially-written or
// missing file would kill ffmpeg, so atomic replacement is mandatory. frontier
// reports how far transcription has committed, logged until the first cue lands so a
// silent gap is visible in --debug.
func cueWriter(ctx context.Context, cuePath string, cues *cue.Builder, frontier func() float64) func(media.Progress) {
	tmpPath := cuePath + ".tmp"
	last := ""
	calls := 0
	wroteCue := false
	return func(sample media.Progress) {
		calls++
		seconds := sample.Position.Seconds()
		lookup := seconds + cueLeadBias
		text := cue.Wrap(cues.CueAt(lookup), cueWrapColumns)
		// Surface the encoder position against how far transcription has
		// reached until the first cue lands, so a silent gap (encoder ahead
		// of the commit frontier, or out_time stuck) is visible in --debug.
		if !wroteCue && (calls == 1 || calls%50 == 0) {
			slog.DebugContext(ctx, "cue writer waiting",
				"calls", calls,
				"out_time", seconds,
				"lookup", lookup,
				"latest_end", frontier(),
				"empty", text == "",
			)
		}
		if text == last {
			return
		}
		if err := os.WriteFile(tmpPath, []byte(text), 0o644); err != nil {
			slog.WarnContext(ctx, "writing subtitle cue", "error", err)
			return
		}
		if err := os.Rename(tmpPath, cuePath); err != nil {
			slog.WarnContext(ctx, "swapping subtitle cue", "error", err)
			return
		}
		if !wroteCue && text != "" {
			slog.InfoContext(ctx, "first subtitle cue rendered", "out_time", seconds, "text", text)
			wroteCue = true
		}
		slog.DebugContext(ctx, "subtitle cue swapped", "out_time", seconds, "text", text)
		last = text
	}
}
