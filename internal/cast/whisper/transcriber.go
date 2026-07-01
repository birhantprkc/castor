// Package whisper runs the whisper.cpp Go bindings against a PCM audio
// stream to produce a growing list of subtitle cues. The audio arrives on an
// io.Reader (16kHz mono s16le — the caller owns whatever process produces
// it); it is sliced into fixed-length windows, each window is fed to the
// in-process whisper model via cgo, and the emitted segments are time-shifted
// and appended to the shared cue list.
package whisper

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	wcpp "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"
)

const (
	// SampleRate is the PCM sample rate whisper expects. The audio fed to
	// Run must be mono s16le at this rate.
	SampleRate  = 16000
	bytesPerSec = SampleRate * 2 // mono, s16le

	// overlapSeconds is how much audio each window shares with the previous
	// one. whisper times segments worst at a window's edges, and a hard,
	// non-overlapping cut splits any utterance straddling the seam — the two
	// halves get transcribed in separate contexts with disagreeing timestamps,
	// which is the dominant source of jittery, two-directional subtitle drift.
	// Overlapping the windows means every utterance is seen whole in at least
	// one of them.
	overlapSeconds = 8

	// trailGuardSeconds is how far back from a window's trailing edge we stop
	// trusting cues. whisper's last segment in a window often runs against
	// truncated/looking-ahead-less audio and is mistimed; the next (overlapped)
	// window re-transcribes that region with full right-context, so we defer
	// committing it. Not applied to the final window at EOF — there is no next
	// window to supersede it.
	trailGuardSeconds = 3
)

// Cue is one subtitle line with absolute timestamps in seconds.
type Cue struct {
	Start float64
	End   float64
	Text  string
}

// Transcriber owns a whisper model and accumulates cues from a PCM stream.
// It is not reusable; call New for each cast.
type Transcriber struct {
	cfg          Config
	modelPath    string
	chunkSeconds int

	mu        sync.Mutex
	cues      []Cue
	latestEnd float64 // highest cue end-time observed, in seconds
	done      bool    // Run has returned; no more cues are coming
}

// New returns a configured Transcriber, resolving the model path (auto-
// downloading the default tiny.en model into the user cache when unset).
func New(ctx context.Context, cfg Config) (*Transcriber, error) {
	modelPath, err := EnsureModel(ctx, cfg.ModelPath)
	if err != nil {
		return nil, err
	}

	chunk := cfg.ChunkSeconds
	if chunk <= 0 {
		// 30s is whisper's native window length: it times segments most
		// accurately with that much context, and anything shorter measurably
		// degrades cue timing.
		chunk = 30
	}

	return &Transcriber{
		cfg:          cfg,
		modelPath:    modelPath,
		chunkSeconds: chunk,
	}, nil
}

// LatestEnd returns the end-time, in seconds, of the last cue transcribed.
func (t *Transcriber) LatestEnd() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.latestEnd
}

func (t *Transcriber) cueCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.cues)
}

// Done reports whether Run has returned, i.e. no further cues will appear.
func (t *Transcriber) Done() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

// CueAt returns the text of the cue active at time tSec, or "" if no cue
// covers that instant. When cues overlap, the latest-starting one wins —
// it is the most specific line for that moment.
func (t *Transcriber) CueAt(tSec float64) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Cues are appended in non-decreasing start order. Find the first cue
	// starting strictly after tSec, then scan left for one still covering it.
	i := sort.Search(len(t.cues), func(i int) bool { return t.cues[i].Start > tSec })
	for j := i - 1; j >= 0; j-- {
		if t.cues[j].End > tSec {
			return t.cues[j].Text
		}
		// Stop scanning once cues end so far back they can't cover tSec.
		// Whisper windows are short, so a small horizon is plenty.
		if tSec-t.cues[j].Start > 60 {
			break
		}
	}
	return ""
}

// Run loads the whisper model and consumes pcm (16kHz mono s16le) until EOF
// or ctx cancellation, feeding each fixed-length window to a fresh whisper
// context. It always marks the transcriber done on return.
func (t *Transcriber) Run(ctx context.Context, pcm io.Reader) error {
	defer func() {
		t.mu.Lock()
		t.done = true
		t.mu.Unlock()
	}()

	slog.InfoContext(ctx, "loading whisper model", "path", t.modelPath)
	model, err := wcpp.New(t.modelPath)
	if err != nil {
		return fmt.Errorf("loading whisper model: %w", err)
	}
	defer model.Close()

	windowBytes := t.chunkSeconds * bytesPerSec
	overlapBytes := overlapSeconds * bytesPerSec
	if overlapBytes >= windowBytes {
		overlapBytes = windowBytes / 2
	}
	stepBytes := windowBytes - overlapBytes

	step := make([]byte, stepBytes)
	// carry holds the trailing overlap of the previous window, prepended to
	// the next read so each window overlaps its predecessor.
	carry := make([]byte, 0, overlapBytes)
	samples := make([]float32, 0, windowBytes/2)

	var windowStartBytes int64 // absolute byte offset of the current window's first sample
	var committedUntil float64 // highest cue end-time already appended, in seconds
	chunkIdx := 0
	lastProgress := time.Now()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, readErr := io.ReadFull(pcm, step)
		atEOF := readErr != nil && (errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF))
		if n > 0 {
			window := append(carry, step[:n]...)
			windowStartSec := float64(windowStartBytes) / float64(bytesPerSec)
			windowEndSec := windowStartSec + float64(len(window))/float64(bytesPerSec)

			samples = pcmToFloat32(window, samples[:0])
			cues, err := t.transcribeChunk(ctx, model, samples, windowStartSec)
			if err != nil {
				slog.WarnContext(ctx, "whisper chunk failed", "index", chunkIdx, "error", err)
			} else {
				// Cues clear of the trailing edge are stable; defer the rest to
				// the next (overlapping) window, which re-transcribes them with
				// full right-context. The final window at EOF has no successor,
				// so everything in it is committed.
				edge := windowEndSec
				if !atEOF {
					edge -= trailGuardSeconds
				}
				committedUntil = t.commitCues(cues, committedUntil, edge)
			}
			chunkIdx++
			if time.Since(lastProgress) >= 15*time.Second {
				slog.InfoContext(ctx, "transcription progress",
					"transcribed_seconds", int(windowEndSec),
					"committed_seconds", int(committedUntil),
					"cue_count", t.cueCount(),
				)
				lastProgress = time.Now()
			}

			// Retain the trailing overlapBytes as the next window's lead-in.
			keep := min(overlapBytes, len(window))
			carry = append(carry[:0], window[len(window)-keep:]...)
			windowStartBytes += int64(len(window) - keep)
		}
		if readErr != nil {
			if atEOF {
				slog.InfoContext(ctx, "transcription finished", "transcribed_seconds", int(committedUntil))
				return nil
			}
			return fmt.Errorf("reading pcm stream: %w", readErr)
		}
	}
}

// transcribeChunk runs whisper on a single audio window and returns its
// segments time-shifted by offset (the window's absolute start, in seconds).
// It does not touch the shared cue list — commitCues decides what to keep.
func (t *Transcriber) transcribeChunk(ctx context.Context, model wcpp.Model, samples []float32, offset float64) ([]Cue, error) {
	wctx, err := model.NewContext()
	if err != nil {
		return nil, fmt.Errorf("new whisper context: %w", err)
	}

	if t.cfg.Language != "" && t.cfg.Language != "auto" {
		if err := wctx.SetLanguage(t.cfg.Language); err != nil {
			slog.WarnContext(ctx, "whisper SetLanguage failed", "language", t.cfg.Language, "error", err)
		}
	}
	if t.cfg.Threads > 0 {
		wctx.SetThreads(uint(t.cfg.Threads))
	}

	if err := wctx.Process(samples, nil, nil, nil); err != nil {
		return nil, fmt.Errorf("whisper process: %w", err)
	}

	var cues []Cue
	for {
		seg, err := wctx.NextSegment()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("whisper segment: %w", err)
		}
		cues = append(cues, Cue{
			Start: seg.Start.Seconds() + offset,
			End:   seg.End.Seconds() + offset,
			Text:  seg.Text,
		})
	}
	return cues, nil
}

// commitCues appends the cues that fall in the (committedUntil, edge] window
// to the shared list and returns the new commit boundary — the highest end-time
// committed. Cues starting before committedUntil were already emitted by a
// previous overlapping window; cues ending past edge are deferred to the next
// window, which re-transcribes them with full right-context. The boundary is
// the last committed cue's end (not edge) so a cue straddling edge — deferred
// here — is still accepted next time rather than falling into the already-
// emitted region and being lost. Cues are appended in non-decreasing start
// order, which CueAt relies on.
func (t *Transcriber) commitCues(cues []Cue, committedUntil, edge float64) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range cues {
		if c.Start < committedUntil || c.End > edge {
			continue
		}
		t.cues = append(t.cues, c)
		if c.End > t.latestEnd {
			t.latestEnd = c.End
		}
		if c.End > committedUntil {
			committedUntil = c.End
		}
	}
	return committedUntil
}

// pcmToFloat32 converts a buffer of signed 16-bit little-endian PCM samples
// to []float32 normalized in [-1.0, 1.0], reusing dst's backing array.
func pcmToFloat32(pcm []byte, dst []float32) []float32 {
	n := len(pcm) / 2
	if cap(dst) < n {
		dst = make([]float32, n)
	} else {
		dst = dst[:n]
	}
	for i := range n {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		dst[i] = float32(s) / 32768.0
	}
	return dst
}
