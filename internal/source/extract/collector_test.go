package extract

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"

	"github.com/stupside/castor/internal/media"
)

// m3u8Pattern is the shipped capture pattern's essential shape (internal/config
// configures `\.m3u8`), so these tests exercise the same admission path a real
// capture takes rather than calling add directly.
var m3u8Pattern = []*regexp.Regexp{regexp.MustCompile(`\.m3u8`)}

func testCollector(t *testing.T) *collector {
	t.Helper()
	return newCollector(context.Background(), m3u8Pattern, 8)
}

// The two documents the grammar has to separate, cut down to the tags that decide it.
// Neither URL nor filename appears in either, which is the point: only the body says
// which one it is.
const (
	masterDocument = "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080\n" +
		"v/1080.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\n" +
		"v/360.m3u8\n"
	mediaDocument = "#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:6\n" +
		"#EXTINF:6.000,\n" +
		"seg_00001.m4s\n" +
		"#EXT-X-ENDLIST\n"
)

// captureWithBody records a candidate the way the browser does, a pattern match
// carrying a request ID, and then feeds it the body Chrome would have handed over. The
// tests go through ladderOf rather than setting the field, so what they pin is the
// grammar production reads and not a value a test invented.
func captureWithBody(t *testing.T, c *collector, u string, reqID network.RequestID, body string) {
	t.Helper()
	c.addByPattern(u, reqID)
	c.noteLadder(reqID, ladderOf(body))
}

// The definition of a master is a tag, not a path. Every row here is a body castor
// would have to judge with no help from the URL, which is the shape that cost three
// runs their ladder: a master served as index.m3u8 scored like any other link.
func TestLadderComesFromTheDocumentsOwnTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want media.Ladder
	}{
		{"a master advertises renditions", masterDocument, media.LadderMultivariant},
		{"a media playlist advertises none", mediaDocument, media.LadderSole},
		{
			// The failure the whole mechanism exists for, stated as a test: the body decides
			// and the name is irrelevant.
			name: "a master with one variant is still a master",
			body: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=18505000,RESOLUTION=3840x1600\nindex.m3u8\n",
			want: media.LadderMultivariant,
		},
		{
			// getResponseBody answers with whatever Chrome holds, which for a hotlink-gated
			// or rate-limited fetch is an error page. Nothing in it is evidence that the
			// source published a single rendition.
			name: "an HTML error page is not a playlist",
			body: "<!doctype html><html><body>403 Forbidden</body></html>",
			want: media.LadderUnknown,
		},
		{"an empty body says nothing", "", media.LadderUnknown},
		{
			// A stray variant tag with no #EXTM3U above it is not a playlist. Reading it as
			// a master would end the collection window on a document with no ladder in it,
			// which is worse than not knowing.
			name: "renditions without a playlist header prove nothing",
			body: "#EXT-X-STREAM-INF:BANDWIDTH=6000000\nv/1080.m3u8\n",
			want: media.LadderUnknown,
		},
		{
			// HLS tags are defined in upper case and matched literally, so this stays
			// unknown rather than becoming a master on a lower-cased body.
			name: "lower-cased tags are not HLS tags",
			body: "#extm3u\n#ext-x-stream-inf:BANDWIDTH=6000000\nv/1080.m3u8\n",
			want: media.LadderUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ladderOf(tc.body); got != tc.want {
				t.Errorf("ladderOf() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The action pipeline and the collection window must agree on what "enough captured"
// means, because when they disagreed the weaker of the two won: the pipeline stopped
// on any hit, so a page that leaked a chunklist during the opening click was never
// driven to request its master. Both read this, and what it reads is the document.
func TestOnlyAConfirmedLadderCountsAsEnoughCaptured(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		body string
		want bool
	}{
		{
			// The run this exists for: the master arrived, the URL named nothing, and castor
			// held it without knowing.
			name: "a master served as index.m3u8",
			url:  "https://cdn.example/hls/index.m3u8",
			body: masterDocument,
			want: true,
		},
		{
			// The inverse, and just as real: the name says master and the body is one
			// rendition. A path is a publisher's habit, not a statement about a document.
			name: "a media playlist named master.m3u8",
			url:  "https://cdn.example/hls/master.m3u8",
			body: mediaDocument,
			want: false,
		},
		{
			// Unknown is not a master. The cost is the early exit, which is seconds of a
			// signed link's life; the cost of the opposite is ending the collection on a
			// chunklist and casting one rendition with nothing to fall back to.
			name: "a master-named URL whose body Chrome would not give up",
			url:  "https://cdn.example/hls/master.m3u8",
			body: "",
			want: false,
		},
		{"a chunklist", "https://cdn.example/hls/chunklist_b18505000.m3u8", mediaDocument, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testCollector(t)
			captureWithBody(t, c, tc.url, "req-1", tc.body)
			if !c.HasHits() {
				t.Fatalf("addByPattern(%q) captured nothing, the test proves nothing", tc.url)
			}
			if got := c.hasMaster(); got != tc.want {
				t.Errorf("hasMaster() = %v, want %v for %q", got, tc.want, tc.url)
			}
		})
	}
}

// The ordering the ranker inherits. A confirmed ladder leads, because the per-host
// probe cap drops the tail of a long capture list unmeasured, and an embed proxy
// publishes a master plus a dozen variants behind one signature: a master ordered by
// its path alone sits in that tail and takes its ladder with it.
func TestAConfirmedLadderLeadsTheCapture(t *testing.T) {
	c := testCollector(t)
	captureWithBody(t, c, "https://cdn.example/hls/master.m3u8", "req-named", mediaDocument)
	captureWithBody(t, c, "https://cdn.example/hls/index.m3u8", "req-real", masterDocument)

	entries := c.Entries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if got := entries[0].RawURL; got != "https://cdn.example/hls/index.m3u8" {
		t.Errorf("head = %q, want the confirmed master; the URL score outranked the document", got)
	}
	if entries[0].Ladder != media.LadderMultivariant {
		t.Errorf("head carries renditions=%v, so nothing downstream can tell it holds a ladder", entries[0].Ladder)
	}
}

// Unknown must not demote, which is the same convention media.Reach's unproven zero
// value and preference's unmeasured height already follow. A document nobody could
// read is compared exactly like one confirmed to hold a single rendition, so the URL
// score still decides between them and a failed body read costs a candidate nothing.
func TestUnknownRenditionsDoNotDemoteACapture(t *testing.T) {
	c := testCollector(t)
	captureWithBody(t, c, "https://cdn.example/hls/index.m3u8", "req-sole", mediaDocument)
	c.addByPattern("https://cdn.example/hls/master.m3u8", "req-unknown")

	entries := c.Entries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if got := entries[0].RawURL; got != "https://cdn.example/hls/master.m3u8" {
		t.Errorf("head = %q, want the unread capture to keep the place its URL earned: a body Chrome would not hand over was ranked below one that read as a single rendition", got)
	}
	if entries[0].Ladder != media.LadderUnknown {
		t.Errorf("the unread capture carries renditions=%v, want unknown", entries[0].Ladder)
	}
}

// What a body read is fired for, and how often. Each condition keeps a cost down: the
// browser finishes hundreds of loads per page, getResponseBody hands back the WHOLE
// body (so asking it for an mp4 candidate pulls the film through the debugging
// protocol), and the answer never changes once it is in.
func TestABodyIsReadOnceAndOnlyForPlaylists(t *testing.T) {
	t.Run("a playlist candidate is claimed exactly once", func(t *testing.T) {
		c := testCollector(t)
		c.addByPattern("https://cdn.example/hls/index.m3u8", "req-1")
		if !c.claimBodyRead("req-1") {
			t.Fatal("the first claim was refused, so no captured playlist is ever read")
		}
		if c.claimBodyRead("req-1") {
			t.Error("a second claim succeeded, so a duplicated loadingFinished pulls the same document twice")
		}
	})

	t.Run("a request belonging to no candidate is not read", func(t *testing.T) {
		c := testCollector(t)
		c.addByPattern("https://cdn.example/hls/index.m3u8", "req-1")
		if c.claimBodyRead("req-page-script") {
			t.Error("a request nothing captured was claimed, so every load on the page costs a body read")
		}
	})

	t.Run("a non-playlist candidate is not read", func(t *testing.T) {
		c := testCollector(t)
		c.addByMIME("https://cdn.example/movie.mp4", "req-mp4", "video/mp4")
		if !c.HasHits() {
			t.Fatal("addByMIME captured nothing, the test proves nothing")
		}
		if c.claimBodyRead("req-mp4") {
			t.Error("an mp4 candidate was claimed, which pulls the whole file through CDP for a question its container cannot answer")
		}
	})

	t.Run("an answered candidate is not read again", func(t *testing.T) {
		c := testCollector(t)
		captureWithBody(t, c, "https://cdn.example/hls/index.m3u8", "req-1", masterDocument)
		if c.claimBodyRead("req-1") {
			t.Error("a candidate whose document was already read was claimed again")
		}
	})
}

// The collection window must not be cut short by a chunklist: a late master is the
// whole reason the window exists, and the puller has a ladder only if it arrives.
func TestCollectionWindowRunsItsCourseWithoutAMaster(t *testing.T) {
	c := testCollector(t)
	captureWithBody(t, c, "https://cdn.example/hls/index-s2160p-v1-a1.m3u8", "req-1", mediaDocument)

	const window = 400 * time.Millisecond
	start := time.Now()
	entries, err := c.Wait(context.Background(), time.Second, window)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	// Half the window is a generous floor: the point is that Wait did not return on
	// the chunklist alone, not the precise scheduling.
	if elapsed < window/2 {
		t.Errorf("Wait returned after %s, less than half the %s window: a chunklist cut the collection short", elapsed, window)
	}
}

// A master is the one thing worth cutting the window short for. These are short-lived
// signed links, so every second held here is a second of token life spent before the
// puller can touch the stream.
func TestCollectionWindowStopsOnAMaster(t *testing.T) {
	c := testCollector(t)
	captureWithBody(t, c, "https://cdn.example/hls/index.m3u8", "req-1", masterDocument)

	const window = 5 * time.Second
	start := time.Now()
	if _, err := c.Wait(context.Background(), time.Second, window); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > window/2 {
		t.Errorf("Wait held for %s with a master in hand, want an immediate return", elapsed)
	}
}

// With nothing captured at all Wait owes the caller a reason rather than an empty
// success, which is what lets ExtractAll name why an embed produced nothing.
func TestCollectionWindowReportsCapturingNothing(t *testing.T) {
	c := testCollector(t)

	entries, err := c.Wait(context.Background(), 50*time.Millisecond, time.Second)
	if err == nil {
		t.Fatalf("Wait succeeded with %d entries and nothing captured", len(entries))
	}
	if entries != nil {
		t.Errorf("Wait returned %d entries alongside its error", len(entries))
	}
}
