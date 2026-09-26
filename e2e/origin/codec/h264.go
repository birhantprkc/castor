// Package codec is the encoders an origin publishes tracks with, each named as ffprobe names its codec.
package codec

// H264 is software x264, fast enough to encode fixtures at test time.
type H264 struct{}

func (H264) Name() string          { return "h264" }
func (H264) EncoderArgs() []string { return []string{"libx264", "-preset", "ultrafast"} }
