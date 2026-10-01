package hls

import (
	"net/url"
	"testing"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/sourcetest"
)

// pick is the rung resolution chooses from a document under a cap.
func pick(doc hlsDocument, ceiling media.HeightCap) source.Rendition {
	return source.Origin{Renditions: ladder(doc, nil)}.Choose(ceiling, byBitrate)
}

func TestPickVariant(t *testing.T) {
	u := func(path string) *url.URL { return &url.URL{Path: path} }
	doc := hlsDocument{variants: []hlsVariant{
		{url: u("/480"), bandwidth: 1_000_000, height: 480, hasVideo: true},
		{url: u("/1080"), bandwidth: 6_000_000, height: 1080, hasVideo: true},
		{url: u("/2160"), bandwidth: 20_000_000, height: 2160, hasVideo: true},
		{url: u("/audio"), bandwidth: 30_000_000},
	}}
	for _, tt := range []struct {
		maxHeight media.HeightCap
		want      string
	}{{1080, "/1080"}, {240, "/480"}} {
		if got := pick(doc, tt.maxHeight).URL.Path; got != tt.want {
			t.Errorf("pick(max=%d) = %q, want %q", tt.maxHeight, got, tt.want)
		}
	}
}

// The shape that silently loses audio: video-only variants, each paired with its own audio group.
func TestParsePlaylistPairsEachVariantWithItsAudioGroup(t *testing.T) {
	body := "#EXTM3U\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud-low",NAME="Low",DEFAULT=YES,URI="audio/low.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud-high",NAME="High",DEFAULT=YES,URI="audio/high.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480,CODECS="avc1.4d401f,mp4a.40.2",AUDIO="aud-low"` + "\n480.m3u8\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud-high"` + "\n1080.m3u8\n"
	base := sourcetest.URL(t, "http://example.com/master.m3u8")
	master, err := parsePlaylist(body, base, base)
	if err != nil {
		t.Fatal(err)
	}
	rungs := ladder(master, nil)
	if len(rungs) != 2 || rungs[0].AudioURL == nil || rungs[1].AudioURL == nil {
		t.Fatalf("ladder = %+v, want two rungs each paired with companion audio", rungs)
	}
	if rungs[0].AudioURL.Path != "/audio/low.m3u8" || rungs[1].AudioURL.Path != "/audio/high.m3u8" {
		t.Errorf("audio = %s / %s, want each rung's own group", rungs[0].AudioURL.Path, rungs[1].AudioURL.Path)
	}
}
