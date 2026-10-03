package media

// Bitrate is bits per second; only a declared rate is a ceiling for choosing, never a measured one.
type Bitrate int64

// Speed is media seconds per wall-clock second (ffmpeg's speed= field).
type Speed float64
