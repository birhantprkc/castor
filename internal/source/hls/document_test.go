package hls

import (
	"slices"
	"testing"

	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/sourcetest"
)

var parse = source.Formats{Format{}}.Parse

const multivariantPlaylist = "#EXTM3U\n" +
	"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",URI=\"audio/eng.m3u8\"\n" +
	"#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080,AUDIO=\"aud\"\n" +
	"v/1080.m3u8\n"

func TestLadderComesFromTheDocumentsOwnSyntax(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want source.Ladder
	}{
		{"a playlist declaring renditions is a master", multivariantPlaylist, source.LadderMultivariant},
		{"a playlist declaring none is one rendition", "#EXTM3U\n#EXTINF:6.000,\nseg.ts\n#EXT-X-ENDLIST\n", source.LadderSole},
		{"an error page is not a document", "<!doctype html><html>403 Forbidden</html>", source.LadderUnknown},
		{"a rendition tag without the signature proves nothing", "#EXT-X-STREAM-INF:BANDWIDTH=6000000\nv/1080.m3u8\n", source.LadderUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := parse(tc.body, sourcetest.URL(t, "https://cdn.example/a/index")); got != tc.want {
				t.Errorf("ladder = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAMasterNamesItsRenditionsAndCompanion(t *testing.T) {
	_, names := parse(multivariantPlaylist, sourcetest.URL(t, "https://cdn.example/hls/index.m3u8"))
	got := make([]string, 0, len(names))
	for _, u := range names {
		got = append(got, u.String())
	}
	slices.Sort(got)
	if want := []string{"https://cdn.example/hls/audio/eng.m3u8", "https://cdn.example/hls/v/1080.m3u8"}; !slices.Equal(got, want) {
		t.Errorf("names = %q, want %q", got, want)
	}
}
