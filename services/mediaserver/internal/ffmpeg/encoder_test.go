package ffmpeg

import (
	"context"
	"os/exec"
	"testing"
)

func TestACancelledProbeIsNotRemembered(t *testing.T) {
	bin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true binary")
	}
	proven := &provenEncoders{ffmpegPath: bin, works: map[string]bool{}}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if proven.available(cancelled, libx264) {
		t.Fatal("a cancelled probe reported the encoder available")
	}
	if !proven.available(t.Context(), libx264) {
		t.Error("a cancelled probe was cached as unavailable")
	}
}
