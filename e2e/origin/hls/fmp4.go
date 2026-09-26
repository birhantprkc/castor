package hls

import "github.com/stupside/castor/e2e/origin"

// FMP4 segments stream as fragmented MP4 behind one init segment per rendition.
type FMP4 struct{}

func (FMP4) Name() string                 { return "hls-fmp4" }
func (FMP4) Supports(origin.Layout) error { return nil }
func (FMP4) Package(dir string, l origin.Layout) origin.Output {
	return pack(dir, l, segments{ext: ".m4s", mime: "video/mp4",
		args: []string{"-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", "init.mp4"}})
}
