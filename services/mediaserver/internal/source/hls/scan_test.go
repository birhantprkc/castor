package hls

import (
	"net/url"
	"testing"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

func TestEverySegmentCarriesTheTagsThatApplyToIt(t *testing.T) {
	const body = "\uFEFF#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:2\n" +
		"#EXT-X-MEDIA-SEQUENCE:40\n" +
		"#EXT-X-START:TIME-OFFSET=-6.5,PRECISE=YES\n" +
		`#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@0"` + "\n" +
		`#EXT-X-KEY:METHOD=AES-128,URI="../k?id=1,2"` + "\n" +
		"#EXT-X-BYTERANGE:1000@720\n#EXTINF:2.0,\nfilm.mp4\n" +
		"#EXT-X-BYTERANGE:1000\n#EXTINF:2.0,\nfilm.mp4\n" +
		"#EXT-X-GAP\n#EXTINF:2.0,\ngone.mp4\n" +
		`#EXT-X-KEY:METHOD=AES-128,URI="k2",IV=0xabc` + "\n" +
		"#EXT-X-DISCONTINUITY\n#EXTINF:2.0,\nnext.mp4\n" +
		"#EXT-X-KEY:METHOD=NONE\n#EXTINF:1.5,\nclear.mp4\n" +
		"#EXT-X-ENDLIST\n"
	base, _ := url.Parse("https://cdn.example/live/v/index.m3u8")
	got, err := scanMedia(body, base)
	if err != nil {
		t.Fatal(err)
	}
	init := &timeline.Map{URI: "https://cdn.example/live/v/init.mp4", Range: timeline.Range{Offset: 0, Length: 720}}
	want := []timeline.Segment{
		{URI: "https://cdn.example/live/v/film.mp4", Duration: 2 * time.Second, Range: timeline.Range{Offset: 720, Length: 1000}, Map: init,
			Key:   timeline.Key{Method: "AES-128", URI: "https://cdn.example/live/k?id=1,2", IV: "0x00000000000000000000000000000028"},
			Place: timeline.Place{Start: 40, End: 41}},
		// A range with no offset continues the resource's last one.
		{URI: "https://cdn.example/live/v/film.mp4", Duration: 2 * time.Second, Range: timeline.Range{Offset: 1720, Length: 1000}, Map: init,
			Key:   timeline.Key{Method: "AES-128", URI: "https://cdn.example/live/k?id=1,2", IV: "0x00000000000000000000000000000029"},
			Place: timeline.Place{Start: 41, End: 42}},
		// The gap at 42 is dropped; its number stays spent.
		{URI: "https://cdn.example/live/v/next.mp4", Duration: 2 * time.Second, Map: init, Seam: true,
			Key:   timeline.Key{Method: "AES-128", URI: "https://cdn.example/live/v/k2", IV: "0xabc"},
			Place: timeline.Place{Start: 43, End: 44}},
		{URI: "https://cdn.example/live/v/clear.mp4", Duration: 1500 * time.Millisecond, Map: init,
			Place: timeline.Place{Start: 44, End: 45}},
	}
	if len(got.segments) != len(want) {
		t.Fatalf("read %d segments, want %d: %+v", len(got.segments), len(want), got.segments)
	}
	for i := range want {
		g, w := got.segments[i], want[i]
		if g.URI != w.URI || g.Duration != w.Duration || g.Range != w.Range || *g.Map != *w.Map || g.Key != w.Key || g.Seam != w.Seam || g.Place != w.Place {
			t.Errorf("segment %d = %+v (map %+v), want %+v", i, g, *g.Map, w)
		}
	}
	if !got.closed || got.start == nil || *got.start != (timeline.Start{Offset: -6500 * time.Millisecond, Precise: true}) {
		t.Errorf("closed %v, start %+v", got.closed, got.start)
	}
}

func TestAPageThatIsNotAPlaylistIsNotReadAsOne(t *testing.T) {
	if _, err := scanMedia("<html>not found</html>", &url.URL{}); err == nil {
		t.Error("read an HTML page as a media playlist")
	}
}
