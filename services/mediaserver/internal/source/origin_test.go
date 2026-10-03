package source

import (
	"net/url"
	"slices"
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/media"
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
