package presentation

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Eyevinn/dash-mpd/mpd"

	"github.com/stupside/castor/e2e/origin"
)

// RenamesPerPeriod gives every Period's representations their own ids, as a stitcher assembling Periods from separate packagers does.
type RenamesPerPeriod struct{}

func (RenamesPerPeriod) Name() string { return "renames-per-period" }

func (RenamesPerPeriod) Wrap(next http.Handler, _ origin.Published) http.Handler {
	return rewrites(next, func(m *mpd.MPD) error {
		renamed := false
		for n, r := range representations(m) {
			rename(r, fmt.Sprintf("p%d-%s", n, r.Id))
			renamed = true
		}
		if !renamed {
			return errors.New("renames-per-period: the presentation names no representation to rename")
		}
		return nil
	})
}

// rename gives a representation another id; its files keep their names, so its template names them outright.
func rename(r *mpd.RepresentationType, id string) {
	if t := r.SegmentTemplate; t != nil {
		t.Media = strings.ReplaceAll(t.Media, "$RepresentationID$", r.Id)
		t.Initialization = strings.ReplaceAll(t.Initialization, "$RepresentationID$", r.Id)
	}
	r.Id = id
}
