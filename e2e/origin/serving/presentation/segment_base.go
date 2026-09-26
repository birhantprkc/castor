package presentation

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"

	"github.com/Eyevinn/dash-mpd/mpd"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/stupside/castor/e2e/origin"
)

// SegmentBase rewrites a single-file presentation's segment lists into a SegmentBase, so a reader must find the segments in the file's own index.
type SegmentBase struct{}

func (SegmentBase) Name() string { return "segment-base" }

func (SegmentBase) Wrap(next http.Handler, _ origin.Published) http.Handler {
	return rewrites(next, func(m *mpd.MPD) error {
		indexed := false
		for _, r := range representations(m) {
			if len(r.BaseURLs) == 0 || r.SegmentList == nil {
				continue
			}
			file := string(r.BaseURLs[0].Value)
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+path.Clean(file), nil))
			at, size, err := sidxAt(rec.Body.Bytes())
			if err != nil {
				return fmt.Errorf("segment-base: %s: %w", file, err)
			}
			r.SegmentList = nil
			r.SetSegmentBase(at, size, false)
			indexed = true
		}
		if !indexed {
			return errors.New("segment-base: the presentation lists no segments by byte range to index")
		}
		return nil
	})
}

// sidxAt is where the file's top-level segment index box sits and how long it is.
func sidxAt(b []byte) (uint32, uint32, error) {
	f, err := mp4.DecodeFile(bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	var at uint64
	for _, box := range f.Children {
		if box.Type() == "sidx" {
			return uint32(at), uint32(box.Size()), nil
		}
		at += box.Size()
	}
	return 0, 0, errors.New("no top-level sidx box")
}
