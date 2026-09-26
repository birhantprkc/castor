// Package player is how a receiver consumes a handed stream: a playlist through a demuxer, anything else as a file.
package player

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/stupside/castor/e2e/receiver"
)

// Playlist plays HLS and DASH the way a TV does: fetching the playlist's segments itself.
type Playlist struct{}

func (Playlist) Name() string { return "playlist" }

func (Playlist) Plays(head []byte) bool {
	return bytes.HasPrefix(head, []byte("#EXTM3U")) || bytes.Contains(head, []byte("<MPD"))
}

// Record copies every stream to Matroska, which carries any codec a playlist can; a stop interrupts ffmpeg so the tape is finalised.
func (Playlist) Record(ctx context.Context, rec receiver.Recording) (string, error) {
	tape := rec.Tape + ".mkv"
	pace := []string{}
	if rec.Viewer.Realtime() {
		pace = []string{"-re"}
	}
	args := slices.Concat([]string{"-hide_banner", "-loglevel", "error", "-user_agent", receiver.UserAgent}, pace,
		[]string{"-i", rec.URL, "-map", "0", "-c", "copy", "-f", "matroska", tape})
	cmd := exec.CommandContext(ctx, rec.FFmpeg, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		return tape, fmt.Errorf("%w: %s", err, out)
	}
	return tape, nil
}
