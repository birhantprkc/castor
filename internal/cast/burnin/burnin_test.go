package burnin

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/subtitle/cue"
	"github.com/stupside/castor/internal/media"
)

// TestTheCueFileHoldsTheLineForTheFrameBeingEncoded covers the burn-in mechanism at
// the seam the encoder's telemetry moved across: the writer places cues from
// -progress SAMPLES rather than from the feed itself, and drawtext reads the file it
// writes once per frame.
//
// It asserts the lookup, not just that something was written. The sample position is
// the encoder's MUX position and the frames being drawn are an encoder lookahead
// ahead of it, so a writer that looked up the cue at the position it was handed would
// place every line late (see cueLeadBias): the first sample here lands inside the cue
// only because of the bias.
func TestTheCueFileHoldsTheLineForTheFrameBeingEncoded(t *testing.T) {
	path, cues := cueFixture(t)
	write := cueWriter(t.Context(), path, cues, func() float64 { return 10 })

	// Mux position 1.5s, so the frame being drawn is around 2.5s, which is inside the
	// committed cue (2.0 to 4.0, trimmed inward to hug the audio).
	write(media.Progress{Position: 1500 * time.Millisecond, Speed: 1.15})
	if got := readFile(t, path); got != "Hello." {
		t.Errorf("cue file = %q, want the line covering the frame being encoded", got)
	}

	// Past the cue, the file must go empty rather than keep the last line on screen.
	write(media.Progress{Position: 5 * time.Second, Speed: 1.15})
	if got := readFile(t, path); got != "" {
		t.Errorf("cue file = %q after the cue ended, want it cleared", got)
	}

	// The swap is a rename, so nothing partial is ever visible at the path drawtext
	// re-opens; a leftover temp file means the writer wrote in place instead.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temp file survived the swap, so the update was not a rename")
	}
}

// TestAnUnchangedLineIsNotRewritten pins the other half of that: at ten samples a
// second, rewriting the same line every time is a rename per tick for a file ffmpeg
// re-opens per frame. The file is removed after the first swap, so a second write
// would recreate it.
func TestAnUnchangedLineIsNotRewritten(t *testing.T) {
	path, cues := cueFixture(t)
	write := cueWriter(t.Context(), path, cues, func() float64 { return 10 })

	sample := media.Progress{Position: 1500 * time.Millisecond}
	write(sample)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(sample)
	if _, err := os.Stat(path); err == nil {
		t.Error("the same line was written twice")
	}
}

// cueFixture is an existing (empty) cue file and one committed cue running from 2.0 to
// 4.0 seconds. The file exists because drawtext opens it before every frame and ffmpeg
// dies on a read that fails, which is why Inputs creates it before the encoder starts.
func cueFixture(t *testing.T) (string, *cue.Builder) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cue.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cues := cue.NewBuilder()
	cues.Commit([]cue.Word{{Start: 2, End: 4, Text: "Hello."}}, 10)
	return path, cues
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
