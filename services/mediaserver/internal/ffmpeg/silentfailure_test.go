package ffmpeg

import (
	"fmt"
	"slices"
	"testing"
)

func TestSilentFailuresAreCaughtOnAnyLine(t *testing.T) {
	for _, marker := range []string{
		"[mpegts @ 0x1] AAC bitstream not in ADTS format and extradata missing",
		"[mp4 @ 0x2] Malformed AAC bitstream detected: use the audio bitstream filter 'aac_adtstoasc' to fix it",
	} {
		t.Run(marker, func(t *testing.T) {
			// The marker scrolls out of the bounded tail and must still be remembered.
			lines := []string{marker}
			for i := range stderrTailCapacity * 2 {
				lines = append(lines, fmt.Sprintf("frame= %d fps=25", i))
			}
			tail, markers := drainLines(t, lines...)
			if markers.failure() == nil {
				t.Error("the marker was forgotten once it left the tail")
			}
			if got := tail.snapshot(); len(got) != stderrTailCapacity || slices.Contains(got, marker) {
				t.Errorf("the tail holds %d lines, want the last %d", len(got), stderrTailCapacity)
			}
		})
	}

	t.Run("a clean transcript reports nothing", func(t *testing.T) {
		if _, markers := drainLines(t, "Input #0, mpegts, from 'pipe:0':", "frame=  250 fps=0.0"); markers.failure() != nil {
			t.Errorf("clean run reported %v", markers.failure())
		}
	})
}
