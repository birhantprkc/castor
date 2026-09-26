package dash

import (
	"errors"
	"path/filepath"

	"github.com/stupside/castor/e2e/origin"
)

// WebM publishes one WebM file per track, each indexed by its own Cues, as ffmpeg's WebM DASH manifest writes it (SegmentBase).
type WebM struct{}

func (WebM) Name() string { return "dash-webm" }

func (WebM) Supports(l origin.Layout) error {
	if !l.Audio {
		return errors.New("dash-webm splits a picture and its sound into two files: give the stream audio")
	}
	return nil
}

func (WebM) Package(dir string, l origin.Layout) origin.Output {
	muxed, video, audio := filepath.Join(dir, "muxed.webm"), filepath.Join(dir, "video.webm"), filepath.Join(dir, "audio.webm")
	const entry = "manifest.mpd"
	split := func(track, to string) []string {
		return []string{"-hide_banner", "-loglevel", "error", "-y", "-i", muxed, "-map", track, "-c", "copy", "-f", "webm", "-dash", "1", "-cluster_time_limit", "1000", to}
	}
	return origin.Output{
		Entry:      entry,
		SegmentExt: ".webm",
		Types:      map[string]string{".mpd": "application/dash+xml", ".webm": "video/webm"},
		Args:       []string{"-f", "webm", muxed},
		Then: [][]string{
			split("0:v", video),
			split("0:a", audio),
			{"-hide_banner", "-loglevel", "error", "-y", "-f", "webm_dash_manifest", "-i", video, "-f", "webm_dash_manifest", "-i", audio,
				"-c", "copy", "-map", "0", "-map", "1", "-f", "webm_dash_manifest", "-adaptation_sets", "id=0,streams=0 id=1,streams=1",
				filepath.Join(dir, entry)},
		},
	}
}
