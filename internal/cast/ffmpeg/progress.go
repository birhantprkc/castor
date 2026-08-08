package ffmpeg

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/stupside/castor/internal/media"
)

// WatchProgress parses ffmpeg's -progress key=value feed from r and calls fn with
// one sample per report block. It returns when r is exhausted, which is ffmpeg
// exiting or being killed under it.
//
// speed= is the field this exists for. It is media seconds produced per wall-clock
// second, the one throughput measure that needs no declared bitrate, and it is what a
// starving read has to be judged on: the run that gave up after one second of picture
// reported speed=0.39x while delivering 7.2 Mbit/s, and 7.2 Mbit/s is a perfectly
// healthy number against every rate that source published (none). The other number
// castor used to have, bytes per second off the spool, printed 0 and could not say
// whether that was a stalled origin or a finished one.
//
// Only out_time_us is trusted for position: out_time_ms famously also holds
// microseconds (a long-standing ffmpeg misnomer) and out_time needs string parsing
// for no benefit. bitrate= is read from the feed and deliberately not carried: it is
// total_size over out_time restated, so keeping it would be a fourth number that can
// disagree with the two it is made of.
//
// Fields carry across blocks rather than resetting, because ffmpeg answers "N/A" for
// every one of them until the first packet is muxed (a real capture of a paced input
// shows five consecutive blocks of out_time_us=N/A speed=N/A), and a sample that read
// an unparseable value as zero would report a reader as having gone backwards.
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
			// End of one report block: emit what it accumulated. The value is
			// "continue" or "end", and both are a complete sample.
			fn(sample)
		}
	}
}

// parseSpeed reads ffmpeg's speed= field, a realtime multiple written with an "x"
// suffix and right-aligned padding ("  79x", "0.109x"), or "N/A" before the first
// packet reaches the muxer.
//
// A value it cannot read is refused rather than reported as zero. Zero is the whole
// tell this feed exists for, so inventing it from a line nobody parsed is how a
// healthy read gets named as starving.
func parseSpeed(value string) (media.Speed, bool) {
	v, err := strconv.ParseFloat(strings.TrimSuffix(value, "x"), 64)
	if err != nil {
		return 0, false
	}
	return media.Speed(v), true
}
