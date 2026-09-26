package presentation

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"

	"github.com/Eyevinn/dash-mpd/mpd"
	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// DashAdPeriod builds a DASH ad break: the film's segments from after on are replaced by a Period of another host's creative on its own init, as in `dash-ad-period: {after: 4, segments: 2, height: 720}`.
type DashAdPeriod struct{}

func (DashAdPeriod) Name() string { return "dash-ad-period" }

func (DashAdPeriod) Build(settings yaml.Node) (origin.Behaviour, error) {
	var a adPeriod
	if err := strategy.Decode(settings, &a); err != nil {
		return nil, fmt.Errorf("dash-ad-period: %w", err)
	}
	if a.After < 1 || a.Segments < 1 || a.Height < 2 {
		return nil, errors.New("dash-ad-period: want after >= 1, segments >= 1 and height >= 2")
	}
	files, sets, err := encodeDashCreative(a.Segments, a.Height)
	if err != nil {
		return nil, fmt.Errorf("dash-ad-period: %w", err)
	}
	a.files, a.sets = files, sets
	return a, nil
}

type adPeriod struct {
	After    int `yaml:"after"`
	Segments int `yaml:"segments"`
	Height   int `yaml:"height"`

	files map[string][]byte
	sets  []*mpd.AdaptationSetType
}

func (adPeriod) Name() string { return "dash-ad-period" }

func (a adPeriod) Wrap(next http.Handler, p origin.Published) http.Handler {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := a.files[path.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(body)
	}))
	go func() {
		<-p.Closing
		cdn.Close()
	}()
	cut := periods{a.After, a.Segments, 1}
	return rewrites(next, func(m *mpd.MPD) error {
		if err := cut.cut(m); err != nil {
			return err
		}
		ad := m.Periods[1]
		ad.BaseURLs = []*mpd.BaseURLType{mpd.NewBaseURL(cdn.URL + "/ad/")}
		ad.AdaptationSets = a.sets
		return nil
	})
}

// encodeDashCreative packages the ad once as DASH, returning its files by name and its adaptation sets.
func encodeDashCreative(segments, height int) (map[string][]byte, []*mpd.AdaptationSetType, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "castor-e2e-dash-ad-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	width := (height*16/9 + 1) &^ 1
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("smptebars=size=%dx%d:rate=15", width, height),
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-t", strconv.Itoa(segments),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "15",
		"-c:a", "aac", "-ac", "2",
		"-f", "dash", "-seg_duration", "1", "-adaptation_sets", "id=0,streams=v id=1,streams=a",
		"-init_seg_name", "ad-init-$RepresentationID$.m4s", "-media_seg_name", "ad-$RepresentationID$-$Number%05d$.m4s",
		filepath.Join(dir, "ad.mpd"))
	if log, err := cmd.CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("encoding the creative: %w\n%s", err, log)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if files[e.Name()], err = os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
			return nil, nil, err
		}
	}
	creative, err := mpd.MPDFromBytes(files["ad.mpd"])
	if err != nil {
		return nil, nil, fmt.Errorf("reading the creative's MPD: %w", err)
	}
	if len(creative.Periods) == 0 || len(creative.Periods[0].AdaptationSets) == 0 {
		return nil, nil, errors.New("the creative's MPD has no adaptation set")
	}
	// A creative packaged on its own names its representations its own way, so no id of the film's reaches into it.
	for _, r := range representations(creative) {
		rename(r, "ad-"+r.Id)
	}
	return files, creative.Periods[0].AdaptationSets, nil
}
