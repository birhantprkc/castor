// Package dash publishes a stream as a DASH presentation: every video rendition in one adaptation set, audio in another.
package dash

import (
	"cmp"
	"errors"
	"path/filepath"

	"github.com/stupside/castor/e2e/origin"
)

type Packager struct{}

func (Packager) Name() string { return "dash" }

// Supports refuses muxed audio: ffmpeg's DASH muxer writes one representation per stream.
func (Packager) Supports(l origin.Layout) error {
	if l.Audio && l.Carriage != origin.Separate {
		return errors.New("dash publishes audio in its own adaptation set: set stream.audio.carriage to separate")
	}
	return nil
}

func (Packager) Package(dir string, l origin.Layout) origin.Output {
	sets := "id=0,streams=v"
	if l.Audio {
		sets += " id=1,streams=a"
	}
	ext := cmp.Or(l.SegmentExt, ".m4s")
	const entry = "manifest.mpd"
	return origin.Output{
		Entry:      entry,
		SegmentExt: ext,
		Types:      map[string]string{".mpd": "application/dash+xml", ext: "video/mp4"},
		Args: []string{"-f", "dash", "-seg_duration", "1", "-adaptation_sets", sets,
			"-init_seg_name", "init-$RepresentationID$" + ext, "-media_seg_name", "chunk-$RepresentationID$-$Number%05d$" + ext,
			filepath.Join(dir, entry)},
	}
}

func (Packager) Muxes() string { return "mp4" }
