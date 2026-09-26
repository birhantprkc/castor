package hls

import "github.com/stupside/castor/e2e/origin"

// TS segments stream as MPEG-TS.
type TS struct{}

func (TS) Name() string                 { return "hls-ts" }
func (TS) Supports(origin.Layout) error { return nil }
func (TS) Package(dir string, l origin.Layout) origin.Output {
	return pack(dir, l, segments{ext: ".ts", mime: "video/mp2t"})
}
