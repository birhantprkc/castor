package whisper

import (
	"context"
	"io"
	"log/slog"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/subtitle"
)

// Burn is the running transcription: model, committed cues, and the file the encoder reads.
type Burn struct {
	*transcriber
	cues *subtitle.Builder
	file *subtitle.CueFile
}

func New(ctx context.Context, whisperCfg subtitle.Whisper, workDir string) (*Burn, error) {
	tr, err := newTranscriber(ctx, whisperCfg)
	if err != nil {
		return nil, err
	}
	cues := &subtitle.Builder{}
	return &Burn{
		transcriber: tr,
		cues:        cues,
		file:        subtitle.NewCueFile(workDir, cues),
	}, nil
}

// Run transcribes pcm until it ends; on failure it drains pcm so the reader never blocks on it.
func (b *Burn) Run(ctx context.Context, pcm io.ReadCloser) {
	defer pcm.Close()
	if err := b.transcriber.Run(ctx, pcm, b.cues); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "transcription failed; subtitles stop here", "error", err)
		_, _ = io.Copy(io.Discard, pcm)
	}
}

// Inputs creates the live cue file and returns its path; the video decision's burn-in input.
func (b *Burn) Inputs() (string, error) { return b.file.Create() }

func (b *Burn) Follow(ctx context.Context) func(media.Progress) { return b.file.Writer(ctx) }
