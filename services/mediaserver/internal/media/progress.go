package media

import "time"

// Progress is one sample of a running ffmpeg's -progress feed.
type Progress struct {
	Position time.Duration
	Bytes    int64
	Speed    Speed
}

// Speed is media seconds per wall-clock second (ffmpeg's speed= field).
type Speed float64
