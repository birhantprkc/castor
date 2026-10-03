package dash

import (
	"path/filepath"

	"github.com/stupside/castor/e2e/origin"
)

// SingleFile writes each representation as one file, its segments listed by byte range behind a global index.
type SingleFile struct{}

func (SingleFile) Name() string { return "dash-single-file" }

func (SingleFile) Supports(l origin.Layout) error { return Packager{}.Supports(l) }

func (SingleFile) Package(dir string, l origin.Layout) origin.Output {
	sets := "id=0,streams=v"
	if l.Audio {
		sets += " id=1,streams=a"
	}
	const entry = "manifest.mpd"
	return origin.Output{
		Entry:      entry,
		SegmentExt: ".mp4",
		Types:      map[string]string{".mpd": "application/dash+xml", ".mp4": "video/mp4"},
		Args: []string{"-f", "dash", "-seg_duration", "1", "-adaptation_sets", sets,
			"-single_file", "1", "-global_sidx", "1", "-use_template", "0", "-use_timeline", "0",
			"-single_file_name", "rep-$RepresentationID$.mp4", filepath.Join(dir, entry)},
	}
}

func (SingleFile) Muxes() string { return "mp4" }
