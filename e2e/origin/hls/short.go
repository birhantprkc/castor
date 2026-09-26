package hls

import "github.com/stupside/castor/e2e/origin"

// Short cuts TS segments every half second, forcing a keyframe at each cut since the encoder's GOP is a second.
type Short struct{}

func (Short) Name() string                 { return "hls-ts-short" }
func (Short) Supports(origin.Layout) error { return nil }
func (Short) Package(dir string, l origin.Layout) origin.Output {
	return pack(dir, l, segments{ext: ".ts", mime: "video/mp2t",
		args: []string{"-hls_time", "0.5", "-force_key_frames", "expr:gte(t,n_forced*0.5)"}})
}
