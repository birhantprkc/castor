package hls

import (
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/source/sourcetest"
)

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
