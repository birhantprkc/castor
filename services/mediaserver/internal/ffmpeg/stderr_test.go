package ffmpeg

import (
	"slices"
	"strings"
	"testing"
)

// A blocking write into a full stderr pipe stops the encode dead, so an over-long line must not stop the drain.
func TestTheDrainOutlastsALineTooLongToHold(t *testing.T) {
	marker := "[mp4 @ 0x1] Malformed AAC bitstream detected"
	tail, markers := drainLines(t, strings.Repeat("x", stderrLineBuffer*3), marker)
	if markers.failure() == nil || !slices.Contains(tail.snapshot(), marker) {
		t.Error("nothing printed after the over-long line was read")
	}
}

func drainLines(t *testing.T, lines ...string) (*ringTail, *markerWatch) {
	t.Helper()
	tail, markers := newRingTail(), &markerWatch{}
	drainStderr(t.Context(), strings.NewReader(strings.Join(lines, "\n")+"\n"), tail, markers)
	return tail, markers
}
