package extract

import (
	"context"
	"regexp"
	"testing"
	"time"
)

// m3u8Pattern is the shipped capture pattern's essential shape (internal/config
// configures `\.m3u8`), so these tests exercise the same admission path a real
// capture takes rather than calling add directly.
var m3u8Pattern = []*regexp.Regexp{regexp.MustCompile(`\.m3u8`)}

func testCollector(t *testing.T) *collector {
	t.Helper()
	return newCollector(context.Background(), m3u8Pattern, 8)
}

// The action pipeline and the collection window must agree on what "enough captured"
// means, because when they disagreed the weaker of the two won: the pipeline stopped
// on any hit, so a page that leaked a chunklist during the opening click was never
// driven to request its master. rankURL is the score both read, so this pins which
// shapes clear the bar.
func TestOnlyAMasterCountsAsEnoughCaptured(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"master playlist", "https://cdn.example/hls/master.m3u8?token=abc", true},
		{"named playlist is not a master", "https://cdn.example/hls/playlist.m3u8", false},
		{"single-rendition media playlist", "https://cdn.example/hls/index-s2160p-v1-a1.m3u8", false},
		{"chunklist", "https://cdn.example/hls/chunklist_b18505000.m3u8", false},
		{"segment-bearing variant", "https://cdn.example/1080p/media-1.m3u8", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testCollector(t)
			c.addByPattern(tc.url, "")
			if !c.HasHits() {
				t.Fatalf("addByPattern(%q) captured nothing, the test proves nothing", tc.url)
			}
			if got := c.hasMaster(); got != tc.want {
				t.Errorf("hasMaster() = %v, want %v for %q (score %d, masterScore %d)",
					got, tc.want, tc.url, rankURL(tc.url), masterScore)
			}
		})
	}
}

// A "playlist" hit scores 50 and a variant penalty subtracts 50, so no combination of
// bonuses below masterScore can add up to it. That is what makes a single threshold a
// sound master test, and it is the property hasMaster's early exits rest on.
func TestNoNonMasterScoreReachesMasterScore(t *testing.T) {
	nonMasters := []string{
		"https://cdn.example/hls/playlist.m3u8",
		"https://cdn.example/hls/playlist/chunklist.m3u8",
		"https://cdn.example/720p/playlist.m3u8",
		"https://cdn.example/hls/index.m3u8",
		"https://cdn.example/segment/playlist.m3u8",
	}
	for _, u := range nonMasters {
		if score := rankURL(u); score >= masterScore {
			t.Errorf("rankURL(%q) = %d, which passes the masterScore threshold of %d", u, score, masterScore)
		}
	}
}

// The collection window must not be cut short by a chunklist: a late master is the
// whole reason the window exists, and the puller has a ladder only if it arrives.
func TestCollectionWindowRunsItsCourseWithoutAMaster(t *testing.T) {
	c := testCollector(t)
	c.addByPattern("https://cdn.example/hls/index-s2160p-v1-a1.m3u8", "")

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
	c.addByPattern("https://cdn.example/hls/master.m3u8", "")

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
