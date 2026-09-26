package extract

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/dash"
	"github.com/stupside/castor/internal/source/hls"
)

// testCollector reads bodies only through noteDocument; the browser hands over none.
func testCollector(t *testing.T) *collector {
	t.Helper()
	unreadable := func(network.RequestID) ([]byte, error) { return nil, errors.New("no body") }
	return newCollector(source.Formats{hls.Format{}, dash.Format{}}, unreadable, time.Second, time.Second, time.Second)
}

func urls(entries []*source.Candidate) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.URL.String()
	}
	return out
}

const (
	masterDocument = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080\n" +
		"v/1080.m3u8\n"
	mediaDocument = "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000,\nseg_00001.m4s\n#EXT-X-ENDLIST\n"
)

func captureWithBody(c *collector, u string, reqID network.RequestID, body string) {
	c.addByURL(u, reqID)
	c.noteDocument(reqID, body)
}

// A link is captured by its name only when a format says the name is a segmented manifest's.
func TestALinkIsCapturedByNameOnlyAsAManifest(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"https://cdn.example/hls/index.m3u8?token=x", true},
		{"https://cdn.example/dash/stream.mpd?sig=abc", true},
		{"https://embed.example/playlist/abc", false},
		{"https://cdn.example/movie.mp4", false},
	} {
		c := testCollector(t)
		c.addByURL(tc.raw, "req-1")
		if got := c.HasHits(); got != tc.want {
			t.Errorf("addByURL(%q) captured = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// Extraction ranks nothing; the ladder comes from each body, never from the name.
func TestCapturesKeepTheOrderTheyWereFoundIn(t *testing.T) {
	c := testCollector(t)
	captureWithBody(c, "https://cdn.example/hls/master.m3u8", "req-named", mediaDocument)
	captureWithBody(c, "https://cdn.example/hls/index.m3u8", "req-real", masterDocument)
	c.addByURL("https://cdn.example/other/chunklist.m3u8", "req-unread")
	c.addByMIME("https://cdn.example/dash/manifest", "req-mpd", "application/dash+xml")

	want := []string{
		"https://cdn.example/hls/master.m3u8",
		"https://cdn.example/hls/index.m3u8",
		"https://cdn.example/other/chunklist.m3u8",
		"https://cdn.example/dash/manifest",
	}
	entries := c.Entries()
	if got := urls(entries); !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want capture order %v", got, want)
	}
	for i, ladder := range []source.Ladder{source.LadderSole, source.LadderMultivariant, source.LadderUnknown, source.LadderUnknown} {
		if entries[i].Ladder != ladder {
			t.Errorf("%s ladder = %v, want %v", entries[i].URL, entries[i].Ladder, ladder)
		}
	}
	if entries[3].ContentType != media.DASH {
		t.Errorf("%s content type = %q, want the one its MIME type confirmed", entries[3].URL, entries[3].ContentType)
	}
}

// A capture another captured document names is a piece of that program, not a program.
func TestACaptureAnotherDocumentNamesIsDropped(t *testing.T) {
	c := testCollector(t)
	const document = "https://cdn.example/hls/index.m3u8"
	c.addByMIME("https://cdn.example/hls/init.mp4", "req-init", "video/mp4")
	captureWithBody(c, document, "req-doc", "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6.000,\nseg_00001.m4s\n#EXT-X-ENDLIST\n")
	c.addByMIME("https://cdn.example/hls/seg_00001.m4s", "req-seg", "video/mp4")
	c.addByMIME("https://cdn.example/elsewhere/movie.mp4", "req-movie", "video/mp4")

	if got, want := urls(c.Entries()), []string{document, "https://cdn.example/elsewhere/movie.mp4"}; !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

// The body of a redirected request describes, and names relative to, the URL it landed on.
func TestARedirectedDocumentIsReadWhereItLanded(t *testing.T) {
	c := testCollector(t)
	const (
		asked  = "https://embed.example/hls/master.m3u8"
		landed = "https://cdn.example/edge/master.m3u8"
	)
	c.Listen(&network.EventRequestWillBeSent{RequestID: "req-1", Request: &network.Request{URL: asked}})
	c.Listen(&network.EventRequestWillBeSent{RequestID: "req-1", Request: &network.Request{URL: landed}})
	c.Listen(&network.EventResponseReceived{RequestID: "req-1", Response: &network.Response{URL: landed, MimeType: "text/plain"}})

	if !c.claimBodyRead("req-1", 0) {
		t.Fatal("the redirected manifest was never claimed for a read")
	}
	c.noteDocument("req-1", masterDocument)
	c.addByURL("https://cdn.example/edge/v/1080.m3u8", "req-2")

	entries := c.Entries()
	if got := urls(entries); !slices.Equal(got, []string{asked, landed}) {
		t.Fatalf("entries = %v, want both hops and not the variant the landed master names", got)
	}
	if entries[1].Ladder != source.LadderMultivariant || entries[0].Ladder != source.LadderUnknown {
		t.Errorf("ladders = %v on %s, %v on %s, want the master on %s", entries[0].Ladder, asked, entries[1].Ladder, landed, landed)
	}
}

// A store that was never told what it holds answers untyped, and the link's own name says what it is.
func TestAnUntypedResponseIsTypedByItsName(t *testing.T) {
	for _, tc := range []struct {
		raw, mime string
		want      string
	}{
		{"https://bucket.example/movie.mp4", "binary/octet-stream", media.MP4},
		{"https://bucket.example/movie.mp4", "application/octet-stream", media.MP4},
		{"https://bucket.example/blob/abc", "application/octet-stream", ""},
		{"https://cdn.example/poster.mp4", "image/jpeg", ""},
	} {
		c := testCollector(t)
		c.addByMIME(tc.raw, "req-1", tc.mime)
		var got string
		if entries := c.Entries(); len(entries) > 0 {
			got = entries[0].ContentType
		}
		if got != tc.want {
			t.Errorf("%s served as %s captured as %q, want %q", tc.raw, tc.mime, got, tc.want)
		}
	}
}

// adDocument is a closed pre-roll: real media too short to be the title.
const adDocument = "#EXTM3U\n#EXTINF:6.000,\nad_1.ts\n#EXTINF:6.000,\nad_2.ts\n#EXT-X-ENDLIST\n"

func featureDocument() string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i := range 60 {
		fmt.Fprintf(&b, "#EXTINF:6.000,\nseg_%d.ts\n", i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func timedCollector(preRoll time.Duration) *collector {
	unreadable := func(network.RequestID) ([]byte, error) { return nil, errors.New("no body") }
	return newCollector(source.Formats{hls.Format{}}, unreadable, time.Second, 50*time.Millisecond, preRoll)
}

// Collection stops at the window once anything but an ad is held, and at the pre-roll's end when nothing is.
func TestCollectionStopsOnceItHoldsMoreThanAds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		atLeast time.Duration
		atMost  time.Duration
	}{
		{"a feature ends it at the window", featureDocument(), 50 * time.Millisecond, 250 * time.Millisecond},
		{"a playlist of unknown length ends it at the window", "#EXTM3U\n#EXTINF:6.000,\nlive.ts\n", 50 * time.Millisecond, 250 * time.Millisecond},
		{"only ads end it at the pre-roll", adDocument, 400 * time.Millisecond, 700 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := timedCollector(400 * time.Millisecond)
			captureWithBody(c, "https://cdn.example/index.m3u8", "req-1", tc.body)
			start := time.Now()
			if _, err := c.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if took := time.Since(start); took < tc.atLeast || took > tc.atMost {
				t.Errorf("collection took %v, want between %v and %v", took, tc.atLeast, tc.atMost)
			}
		})
	}
}
