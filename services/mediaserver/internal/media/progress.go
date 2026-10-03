package media

import "time"

// Progress is one sample of a running ffmpeg's -progress feed.
type Progress struct {
	Position time.Duration
	Bytes    int64
	Speed    Speed
}
