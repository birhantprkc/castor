package source

import (
	"net/url"
	"slices"
	"testing"

	"github.com/stupside/castor/internal/media"
)

func TestOriginLighterIsHeaviestFirstBelowTheCeiling(t *testing.T) {
	rung := func(path string, bitrate media.Bitrate) Rendition {
		return Rendition{URL: &url.URL{Path: path}, Bitrate: bitrate}
	}
	o := Origin{Renditions: []Rendition{
		rung("/480", 1_400_000), rung("/undeclared", 0), rung("/1080", 6_200_000), rung("/2160", 17_000_000),
	}}
	var got []string
	for _, r := range o.Lighter(17_000_000) {
		got = append(got, r.URL.Path)
	}
	if want := []string{"/1080", "/480"}; !slices.Equal(got, want) {
		t.Errorf("Lighter(17 Mbit/s) = %v, want %v", got, want)
	}
}

// HLS pins the renderer to a rung; DASH lets the renderer choose.
func TestSelfFetchHeightIsWhatARendererCouldChoose(t *testing.T) {
	ladder := Origin{Renditions: []Rendition{{Index: 0, Height: 2160}, {Index: 1, Height: 1080}}}
	if got := SelfFetchHeight(media.Program{}, ladder, Rendition{Index: 1, Height: 1080}); got != 2160 {
		t.Errorf("index-addressed SelfFetchHeight = %d, want the tallest rung 2160", got)
	}
	pinned := Rendition{URL: &url.URL{Path: "/1080.m3u8"}, Height: 1080}
	if got := SelfFetchHeight(media.Program{}, ladder, pinned); got != 1080 {
		t.Errorf("URL-addressed SelfFetchHeight = %d, want the pinned 1080", got)
	}
}
