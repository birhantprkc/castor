// Package burnin is the transcription stage of a cast castor produces the picture for: an
// in-process whisper model fed by the read's PCM tee, whose committed cues are drawn into
// every frame by the encoder's drawtext filter through a live-swapped text file.
//
// It is a package of its own because it is the one part of a cast that needs cgo. Held inside
// the executor it put whisper.cpp in that package's import graph, so the executor and its 3,500
// lines of tests could not be built at all on a host without the submodule compiled ("'whisper.h'
// file not found") while every other cast package built and passed. The executor now names only
// its Stage port, and this mechanism reaches it from the composition root.
package burnin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"

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

// Stage is the running transcription: the model, the cues it commits, and the file the
// encoder draws them from.
type Stage struct {
	tr      *whisper.Transcriber
	builder *cue.Builder
	path    string
}

// New prepares the transcription stage, returning nil when whisper cannot start. That
// downgrades to a subtitle-less cast rather than blocking playback: the cast proceeds without
// burn-in instead of failing outright.
func New(ctx context.Context, whisperCfg subtitle.Whisper, workDir string) *Stage {
	tr, err := whisper.New(ctx, whisperCfg)
	if err != nil {
		slog.WarnContext(ctx, "whisper init failed; casting without subtitles", "error", err)
		return nil
	}
	return &Stage{
		tr:      tr,
		builder: cue.NewBuilder(),
		path:    filepath.Join(workDir, "cue.txt"),
	}
}

// Attach consumes the PCM feed in g until EOF. If transcription fails, the feed keeps
// draining: backpressure on that pipe would otherwise stall the read and starve the buffer the
// encoder is playing from.
func (b *Stage) Attach(ctx context.Context, g *errgroup.Group, pcm io.ReadCloser) {
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
func (b *Stage) Inputs() (string, error) {
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
func (b *Stage) Follow(ctx context.Context) func(media.Progress) {
	return cueWriter(ctx, b.path, b.builder, b.tr.LatestEnd)
}

// Lead is how far transcription has committed, which is what the readiness rule holds a cast
// short of: a burn-in whose cues have not reached the frame being encoded ships a picture with
// nothing drawn on it. Handing back the transcriber itself is what keeps this stage from
// carrying a second frontier that could disagree with it.
func (b *Stage) Lead() watch.Lead { return b.tr }

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
