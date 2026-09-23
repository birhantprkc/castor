package ffmpeg

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/media"
)

// WatchProgress parses ffmpeg's -progress feed; calls fn per report, needs no bitrate.
func WatchProgress(r io.Reader, fn func(media.Progress)) {
	scanner := bufio.NewScanner(r)
	var sample media.Progress
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "out_time_us":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil {
				sample.Position = time.Duration(v) * time.Microsecond
			}
		case "total_size":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil {
				sample.Bytes = v
			}
		case "speed":
			if v, ok := parseSpeed(value); ok {
				sample.Speed = v
			}
		case "progress":
			// progress key ends report block; value is 'continue' or 'end', both complete.
			fn(sample)
		}
	}
}

// parseSpeed reads speed field (realtime multiple with x suffix or N/A); refusal over zero.
func parseSpeed(value string) (media.Speed, bool) {
	v, err := strconv.ParseFloat(strings.TrimSuffix(value, "x"), 64)
	if err != nil {
		return 0, false
	}
	return media.Speed(v), true
}
