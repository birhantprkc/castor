// Package whisper transcribes a cast's sound with whisper.cpp.
package whisper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	wcpp "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"

	"github.com/stupside/castor/services/mediaserver/internal/subtitle"
)

// recognizer runs one loaded whisper model over each window it is handed.
type recognizer struct {
	model        wcpp.Model
	language     string
	vadModelPath string
}

func (r recognizer) Recognize(ctx context.Context, samples []float32, offset float64, prompt string) ([]subtitle.Word, error) {
	wctx, err := r.model.NewContext()
	if err != nil {
		return nil, fmt.Errorf("new whisper context: %w", err)
	}

	// Pinned, since detection is unreliable over music and silence.
	if r.language != "auto" && wctx.IsMultilingual() {
		if err := wctx.SetLanguage(r.language); err != nil {
			slog.WarnContext(ctx, "whisper SetLanguage failed", "language", r.language, "error", err)
		}
	}

	// Silero VAD filters silence; whisper.cpp maps the timestamps back.
	wctx.SetVAD(true)
	wctx.SetVADModelPath(r.vadModelPath)

	// One word per segment, since LocalAgreement matches word by word.
	wctx.SetTokenTimestamps(true)
	wctx.SetSplitOnWord(true)
	wctx.SetMaxSegmentLength(1)

	if prompt != "" {
		wctx.SetInitialPrompt(prompt)
	}

	if err := wctx.Process(samples, nil, nil, nil); err != nil {
		return nil, fmt.Errorf("whisper process: %w", err)
	}

	var words []subtitle.Word
	for {
		seg, err := wctx.NextSegment()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("whisper segment: %w", err)
		}
		if isNoise(seg.Text) {
			continue
		}
		words = append(words, subtitle.Word{
			Start: seg.Start.Seconds() + offset,
			End:   seg.End.Seconds() + offset,
			Text:  seg.Text,
		})
	}
	return words, nil
}

// isNoise is whisper's annotation of what is not speech, or the empty text of borderline audio.
func isNoise(s string) bool {
	if s == "" {
		return true
	}
	return s[0] == '[' || s[0] == '(' || strings.HasPrefix(s, "♪")
}
