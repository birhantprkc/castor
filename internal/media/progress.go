package media

import "time"

// Bitrate is bits per second (declared vs. measured; only declared is ceiling for choice).
type Bitrate int64

// Speed is media seconds per wall-clock second (ffmpeg's speed= field; no bitrate needed).
type Speed float64

// Progress is one sample of reader output (three fields: bytes, position, speed).
type Progress struct {
	Position time.Duration
	Bytes    int64
	Speed    Speed
}
