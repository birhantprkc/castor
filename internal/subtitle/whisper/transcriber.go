// Package whisper transcribes a cast's sound with whisper.cpp.
package whisper

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	wcpp "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"

	"github.com/stupside/castor/internal/subtitle"
)

const (
	// bytesPerSec: feed bytes per second (mono s16le).
	bytesPerSec = subtitle.SampleRate * 2

	// stepSeconds: audio step size (tradeoff: commit sooner vs whisper more).
	stepSeconds = 3

	// The buffer is trimmed at a sentence boundary past the first, and capped short of whisper's 30s window.
	trimAfterSeconds = 15
	maxBufferSeconds = 28

	// promptMaxChars: prompt text limit (restores left context trimmed).
	promptMaxChars = 200
)

// transcriber: whisper wrapper (streams committed words, not reusable).
type transcriber struct {
	language     subtitle.Language
	modelPath    string
	vadModelPath string

	mu        sync.Mutex
	latestEnd float64 // end of the last committed word, in seconds
	done      bool    // Run has returned; no more words are coming
}

// newTranscriber resolves models and auto-downloads defaults if unset.
func newTranscriber(ctx context.Context, cfg subtitle.Whisper) (*transcriber, error) {
	modelPath, err := ensureModel(ctx, cfg.ModelPath)
	if err != nil {
		return nil, err
	}
	vadModelPath, err := ensure(ctx, vadModelName, vadModelBaseURL)
	if err != nil {
		return nil, err
	}

	return &transcriber{
		language:     cfg.Language,
		modelPath:    modelPath,
		vadModelPath: vadModelPath,
	}, nil
}

// LatestEnd: last committed word end-time.
func (t *transcriber) LatestEnd() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.latestEnd
}

func (t *transcriber) setFrontier(sec float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.latestEnd = max(t.latestEnd, sec)
}

// Done reports whether Run has returned, so no further words will appear.
func (t *transcriber) Done() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

func (t *transcriber) markDone() {
	t.mu.Lock()
	t.done = true
	t.mu.Unlock()
}

// Run: LocalAgreement-2 loop (buffer, transcribe, confirm prefix, trim).
func (t *transcriber) Run(ctx context.Context, pcm io.Reader, sink *subtitle.Builder) error {
	defer t.markDone() // runs last: cues are flushed before Done() flips
	defer sink.Close() // runs first: flush the final pending words

	model, err := t.loadModel(ctx)
	if err != nil {
		return err
	}
	defer model.Close()

	step := make([]byte, stepSeconds*bytesPerSec)
	var (
		buf      []float32       // working audio window
		bufStart float64         // absolute time of buf[0], in seconds
		prev     []subtitle.Word // uncommitted tail of the previous hypothesis
		history  []subtitle.Word // committed words still inside the buffer
		prompt   string          // committed text already trimmed out of the buffer
		frontier float64         // absolute end time of the last committed word
	)
	lastProgress := time.Now()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, atEOF, err := readStep(pcm, step)
		if err != nil {
			return err
		}
		buf = appendPCM(buf, step[:n])
		if !atEOF {
			// Trim buffer before transcribe (prevent oversizing).
			buf, bufStart, history, prompt = trimBuffer(ctx, buf, bufStart, history, prompt, frontier)
			// Drop words from trimmed audio (prevent stalling).
			prev = dropCommitted(prev, bufStart)
		}

		var agreed []subtitle.Word
		// Whisper needs 100ms+ (skip if shorter at EOF).
		if len(buf) >= subtitle.SampleRate/10 {
			agreed, prev = t.confirm(ctx, model, buf, bufStart, prompt, prev, frontier, atEOF)
			if len(agreed) > 0 {
				frontier = agreed[len(agreed)-1].End
				history = append(history, agreed...)
				t.setFrontier(frontier)
			}
		}

		sink.Commit(agreed, settledTo(frontier, bufStart, buf, prev))

		if atEOF {
			slog.InfoContext(ctx, "transcription finished", "transcribed_seconds", int(frontier))
			return nil
		}

		if time.Since(lastProgress) >= 15*time.Second {
			slog.InfoContext(ctx, "transcription progress",
				"committed_seconds", int(frontier),
				"buffered_seconds", int(float64(len(buf))/subtitle.SampleRate),
			)
			lastProgress = time.Now()
		}
	}
}

func (t *transcriber) loadModel(ctx context.Context) (wcpp.Model, error) {
	slog.InfoContext(ctx, "loading whisper model", "path", t.modelPath, "vad", t.vadModelPath)
	model, err := wcpp.New(t.modelPath)
	if err != nil {
		return nil, fmt.Errorf("loading whisper model: %w", err)
	}
	return model, nil
}

func readStep(pcm io.Reader, step []byte) (int, bool, error) {
	n, err := io.ReadFull(pcm, step)
	atEOF := err != nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF))
	if err != nil && !atEOF {
		return n, false, fmt.Errorf("reading pcm stream: %w", err)
	}
	return n, atEOF, nil
}

func (t *transcriber) confirm(ctx context.Context, model wcpp.Model, buf []float32, bufStart float64, prompt string, prev []subtitle.Word, frontier float64, atEOF bool) (agreed, tail []subtitle.Word) {
	words, err := t.transcribeBuffer(ctx, model, buf, bufStart, prompt)
	if err != nil {
		slog.WarnContext(ctx, "whisper inference failed", "error", err)
		return nil, prev
	}
	fresh := dropCommitted(words, frontier)
	// Agree on prefix (final window fully committed).
	if atEOF {
		return fresh, prev
	}
	agreed = agreedPrefix(prev, fresh)
	return agreed, fresh[len(agreed):]
}

// settledTo: settlement limit (tail=frontier, no tail=buffer end).
func settledTo(frontier, bufStart float64, buf []float32, tail []subtitle.Word) float64 {
	if len(tail) == 0 {
		return bufStart + float64(len(buf))/subtitle.SampleRate
	}
	return frontier
}

// transcribeBuffer: run whisper on buffer, one word per segment, shift by offset.
func (t *transcriber) transcribeBuffer(ctx context.Context, model wcpp.Model, samples []float32, offset float64, prompt string) ([]subtitle.Word, error) {
	wctx, err := model.NewContext()
	if err != nil {
		return nil, fmt.Errorf("new whisper context: %w", err)
	}

	// Pin language (auto-detect unreliable for music/silence).
	if !t.language.AutoDetect() && wctx.IsMultilingual() {
		if err := wctx.SetLanguage(string(t.language)); err != nil {
			slog.WarnContext(ctx, "whisper SetLanguage failed", "language", t.language, "error", err)
		}
	}

	// Silero VAD filters (whisper.cpp maps timestamps back).
	wctx.SetVAD(true)
	wctx.SetVADModelPath(t.vadModelPath)

	// One word per segment (LocalAgreement matches word-by-word).
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

// dropCommitted skips words before cutoff (committed or trimmed).
func dropCommitted(words []subtitle.Word, cutoff float64) []subtitle.Word {
	i := 0
	for i < len(words) && (words[i].Start+words[i].End)/2 < cutoff {
		i++
	}
	return words[i:]
}

// agreedPrefix: longest agreed prefix (return current for fresher timestamps).
func agreedPrefix(prev, cur []subtitle.Word) []subtitle.Word {
	i := 0
	for i < min(len(prev), len(cur)) && sameWord(prev[i], cur[i]) {
		i++
	}
	return cur[:i]
}

func sameWord(a, b subtitle.Word) bool {
	na, nb := normalizeWord(a.Text), normalizeWord(b.Text)
	if na == "" && nb == "" {
		return a.Text == b.Text
	}
	return na == nb
}

// normalizeWord strips case and edge punctuation (cosmetic agreement).
func normalizeWord(s string) string {
	return strings.TrimFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// isNoise detects annotations and VAD borderline audio.
func isNoise(s string) bool {
	if s == "" {
		return true
	}
	return s[0] == '[' || s[0] == '(' || strings.HasPrefix(s, "♪")
}

// trimBuffer drops old audio at sentence boundary or hard cap; text to prompt.
func trimBuffer(ctx context.Context, buf []float32, bufStart float64, history []subtitle.Word, prompt string, frontier float64) ([]float32, float64, []subtitle.Word, string) {
	dur := float64(len(buf)) / subtitle.SampleRate
	if dur <= trimAfterSeconds {
		return buf, bufStart, history, prompt
	}

	var cut float64
	for _, w := range history {
		if subtitle.SentenceEnd(w.Text) {
			cut = max(cut, w.End)
		}
	}
	if dur > maxBufferSeconds {
		cut = max(cut, frontier)
		if hardMin := bufStart + dur - maxBufferSeconds; cut < hardMin {
			slog.WarnContext(ctx, "dropping unconfirmed audio", "seconds", hardMin-cut)
			cut = hardMin
		}
	}
	if cut <= bufStart {
		return buf, bufStart, history, prompt
	}

	n := min(int((cut-bufStart)*subtitle.SampleRate), len(buf))
	buf = append(buf[:0], buf[n:]...)
	bufStart += float64(n) / subtitle.SampleRate

	var b strings.Builder
	b.WriteString(prompt)
	i := 0
	for ; i < len(history) && history[i].End <= bufStart; i++ {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(history[i].Text)
	}
	history = history[i:]
	prompt = b.String()
	if len(prompt) > promptMaxChars {
		cutIdx := len(prompt) - promptMaxChars
		if sp := strings.IndexByte(prompt[cutIdx:], ' '); sp >= 0 {
			cutIdx += sp + 1
		}
		prompt = prompt[cutIdx:]
	}
	return buf, bufStart, history, prompt
}

// appendPCM converts signed 16-bit little-endian PCM samples to float32 in [-1.0, 1.0].
func appendPCM(dst []float32, pcm []byte) []float32 {
	for i := range len(pcm) / 2 {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		dst = append(dst, float32(s)/32768.0)
	}
	return dst
}
