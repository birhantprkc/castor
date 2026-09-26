package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Repackager copies one fMP4 fragment, its init section ahead of it, into MPEG-TS, where a picture's parameters travel in-band.
func Repackager(ffmpegPath string) func(ctx context.Context, fmp4 io.Reader, ts io.Writer) error {
	return func(ctx context.Context, fmp4 io.Reader, ts io.Writer) error {
		cmd := exec.CommandContext(ctx, ffmpegPath,
			"-hide_banner", "-nostats", "-v", "error",
			"-f", FormatMP4, "-i", StdinPipe,
			"-map", "0:v?", "-map", "0:a?", "-c", "copy",
			// The fragment's own decode times, so consecutive fragments stay one timeline.
			"-copyts",
			// One fixed lift for every fragment: a per-fragment shift of priming samples below zero would overlap its neighbour.
			"-avoid_negative_ts", "disabled", "-output_ts_offset", "10",
			// Each file restarts its continuity counters; flagged, the reader does not drop packets as corrupt.
			"-mpegts_flags", "+initial_discontinuity",
			"-muxdelay", "0", "-muxpreload", "0",
			"-f", FormatMPEGTS, StdoutPipe,
		)
		var stderr strings.Builder
		cmd.Stdin, cmd.Stdout, cmd.Stderr = fmp4, ts, &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("repackaging to MPEG-TS: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}
