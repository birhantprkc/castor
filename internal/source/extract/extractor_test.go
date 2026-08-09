package extract

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/stupside/castor/internal/media"
)

func testExtractor(t *testing.T) *Extractor {
	t.Helper()
	e, err := New(Config{Capture: CaptureConfig{
		Patterns:       []string{`\.m3u8`},
		MaxCandidates:  8,
		MaxConcurrency: 2,
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func testStream(t *testing.T, raw string) *media.Stream {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return &media.Stream{URL: u, ContentType: media.HLS}
}

// What the browser established has to reach the stream, because nothing downstream can
// establish it again: the ladder was read from a response body only Chrome held, and the
// session is gone by the time the ranker runs. A capture whose renditions are unknown
// carries unknown, which the ranker reads leniently rather than as "one rendition".
func TestCapturedFactsReachTheStream(t *testing.T) {
	streams := streamsFrom(context.Background(), []capturedStream{
		{RawURL: "https://cdn.example/hls/index.m3u8", Ladder: media.LadderMultivariant},
		{RawURL: "https://cdn.example/hls/chunklist.m3u8", Ladder: media.LadderSole},
		{RawURL: "https://cdn.example/hls/unread.m3u8"},
		// Neither the extension nor a confirmed MIME names a container here, so there is
		// no reader to point at it.
		{RawURL: "https://cdn.example/player/embed"},
	})

	if len(streams) != 3 {
		t.Fatalf("got %d streams, want 3: %v", len(streams), streams)
	}
	for i, want := range []media.Ladder{media.LadderMultivariant, media.LadderSole, media.LadderUnknown} {
		if streams[i].Ladder != want {
			t.Errorf("%s carries renditions=%v, want %v", streams[i].URL, streams[i].Ladder, want)
		}
	}
}

// Every embed failing used to be an empty success, which surfaced as the ranker's "no
// streams to rank" two calls later and named nothing the user could act on. The causes
// differ per embed and each one implies a different next move, so all of them travel.
func TestEveryURLFailingNamesEveryCause(t *testing.T) {
	e := testExtractor(t)
	urls := []string{"https://a.example/embed", "https://b.example/embed", "https://c.example/embed"}

	causes := map[string]error{
		urls[0]: errors.New("navigation timed out after 30s"),
		urls[1]: errors.New("no stream URL captured within grace period"),
		urls[2]: errors.New("no usable streams found (2 entries captured, none with recognized content type)"),
	}

	streams, err := e.extractAll(context.Background(), urls, func(_ context.Context, target string) ([]*media.Stream, error) {
		return nil, causes[target]
	})

	if streams != nil {
		t.Fatalf("got %d streams, want none", len(streams))
	}
	if err == nil {
		t.Fatal("extractAll succeeded with every URL failing")
	}
	for target, cause := range causes {
		if !errors.Is(err, cause) {
			t.Errorf("joined error does not carry %s's cause %v", target, cause)
		}
		if !strings.Contains(err.Error(), target) {
			t.Errorf("joined error does not name the failing URL %s: %v", target, err)
		}
	}
}

// One embed succeeding is a success: the URLs are alternate embeds of the same title,
// so the two that timed out are noise and must not become the caller's error.
func TestOneSurvivingURLIsASuccess(t *testing.T) {
	e := testExtractor(t)
	urls := []string{"https://a.example/embed", "https://b.example/embed"}

	streams, err := e.extractAll(context.Background(), urls, func(_ context.Context, target string) ([]*media.Stream, error) {
		if target == urls[1] {
			return []*media.Stream{testStream(t, "https://cdn.example/hls/master.m3u8")}, nil
		}
		return nil, errors.New("navigation timed out after 30s")
	})

	if err != nil {
		t.Fatalf("extractAll: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("got %d streams, want 1", len(streams))
	}
}

// Two embeds of one title serve the same CDN link, and the ranker probes what it is
// given: a duplicate is a second probe against a host maxProbePerHost exists to spare.
func TestTheSameLinkFromTwoEmbedsIsOneStream(t *testing.T) {
	e := testExtractor(t)
	urls := []string{"https://a.example/embed", "https://b.example/embed"}

	streams, err := e.extractAll(context.Background(), urls, func(_ context.Context, target string) ([]*media.Stream, error) {
		return []*media.Stream{testStream(t, "https://cdn.example/hls/master.m3u8?token=abc")}, nil
	})

	if err != nil {
		t.Fatalf("extractAll: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("got %d streams, want 1 after dedup: %v", len(streams), streams)
	}
}

// A wrapped cause must survive the join, since extract's own errors arrive wrapped
// ("waiting for streams on %s: %w") and the innermost one is the actionable fact.
func TestAWrappedCauseSurvivesTheJoin(t *testing.T) {
	e := testExtractor(t)
	inner := errors.New("no stream URL captured within grace period")

	_, err := e.extractAll(context.Background(), []string{"https://a.example/embed"},
		func(_ context.Context, target string) ([]*media.Stream, error) {
			return nil, fmt.Errorf("waiting for streams on %s: %w", target, inner)
		})

	if !errors.Is(err, inner) {
		t.Errorf("joined error lost the wrapped cause %v: %v", inner, err)
	}
}
