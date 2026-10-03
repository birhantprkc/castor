package recovery

import (
	"net/url"
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// HLS pins the device to a rung; DASH lets the device choose.
func TestSelfFetchHeightIsWhatADeviceCouldChoose(t *testing.T) {
	ladder := source.Origin{Renditions: []source.Rendition{{Representation: "4k", Height: 2160}, {Representation: "hd", Height: 1080}}}
	if got := (Attempt{Origin: ladder, Rendition: source.Rendition{Representation: "hd", Height: 1080}}).SelfFetchHeight(); got != 2160 {
		t.Errorf("index-addressed SelfFetchHeight = %d, want the tallest rung 2160", got)
	}
	pinned := source.Rendition{URL: &url.URL{Path: "/1080.m3u8"}, Height: 1080}
	if got := (Attempt{Origin: ladder, Rendition: pinned}).SelfFetchHeight(); got != 1080 {
		t.Errorf("URL-addressed SelfFetchHeight = %d, want the pinned 1080", got)
	}
}
