package source_test

import (
	"testing"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/dash"
	"github.com/stupside/castor/internal/source/hls"
	"github.com/stupside/castor/internal/source/sourcetest"
)

func TestALinkIsNamedByTheFormatThatDeclaresItsName(t *testing.T) {
	formats := source.Formats{hls.Format{}, dash.Format{}}
	for _, tc := range []struct {
		raw, mime, want string
	}{
		{"https://cdn.example/MASTER.M3U8?token=x", "", media.HLS},
		{"https://cdn.example/manifest.mpd", "", media.DASH},
		{"https://cdn.example/manifest", "application/dash+xml", media.DASH},
		{"https://cdn.example/index", "Application/VND.Apple.MPEGURL", media.HLS},
		{"https://cdn.example/movie.mp4", "", media.MP4},
		// The name the link spells wins over the type the server claims.
		{"https://cdn.example/master.m3u8", "video/mp4", media.HLS},
		{"https://cdn.example/player/embed", "text/html", ""},
	} {
		if got := formats.ContentTypeOf(sourcetest.URL(t, tc.raw), tc.mime); got != tc.want {
			t.Errorf("ContentTypeOf(%s, %q) = %q, want %q", tc.raw, tc.mime, got, tc.want)
		}
	}
}
