package subtitle

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

const (
	// cueLeadBias compensates for encoder lookahead (Whisper ~±0.5s accurate).
	cueLeadBias = 1.0

	// cueWrapColumns: broadcast convention (two lines in safe area).
	cueWrapColumns = 42
)

// CueFile is the live subtitle text file the encoder reads, swapped as cues commit.
type CueFile struct {
	path string
	cues *Cues
}

// NewCueFile names the cue file castor keeps inside dir. Nothing is written until Create.
func NewCueFile(dir string, cues *Cues) *CueFile {
	return &CueFile{path: filepath.Join(dir, "cue.txt"), cues: cues}
}

// Create file before encoder (drawtext dies on read failure).
func (f *CueFile) Create() (string, error) {
	if err := os.WriteFile(f.path, nil, 0o644); err != nil {
		return "", fmt.Errorf("creating subtitle cue file: %w", err)
	}
	return f.path, nil
}

// Writer keeps file updated with current subtitle; atomic writes (temp+rename).
func (f *CueFile) Writer(ctx context.Context) func(media.Progress) {
	tmpPath := f.path + ".tmp"
	last := ""
	wroteCue := false
	return func(sample media.Progress) {
		seconds := sample.Position.Seconds()
		text := wrap(f.cues.cueAt(seconds+cueLeadBias), cueWrapColumns)
		if text == last {
			return
		}
		if err := os.WriteFile(tmpPath, []byte(text), 0o644); err != nil {
			slog.WarnContext(ctx, "writing subtitle cue", "error", err)
			return
		}
		if err := os.Rename(tmpPath, f.path); err != nil {
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

func wrap(text string, width int) string {
	var b strings.Builder
	lineLen := 0
	for w := range strings.FieldsSeq(text) {
		n := utf8.RuneCountInString(w)
		switch {
		case lineLen == 0:
			b.WriteString(w)
			lineLen = n
		case lineLen+1+n <= width:
			b.WriteByte(' ')
			b.WriteString(w)
			lineLen += 1 + n
		default:
			b.WriteByte('\n')
			b.WriteString(w)
			lineLen = n
		}
	}
	return b.String()
}
