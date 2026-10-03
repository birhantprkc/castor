package origin

import (
	"fmt"
	"os/exec"
	"slices"
	"strconv"
)

// Creative encodes an ad unlike any content, one-second segments of 16:9 bars at height and an 880 Hz tone, as mux writes it.
func Creative(segments, height int, mux ...string) error {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return err
	}
	args := slices.Concat([]string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("smptebars=size=%dx%d:rate=15", width(height), height),
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-t", strconv.Itoa(segments),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "15",
		"-c:a", "aac", "-ac", "2"}, mux)
	if log, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("encoding the creative: %w\n%s", err, log)
	}
	return nil
}
