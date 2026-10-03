package rank

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
	"github.com/stupside/castor/services/mediaserver/internal/source/sourcetest"
)

var testConfig = Config{ProbeMaxConcurrency: 2}

// scripted measures every link its script names as opened, records what it was asked, and fails the rest.
type scripted struct {
	answers map[string]*media.ProbeInfo

	mu    sync.Mutex
	asked []string
}

func (s *scripted) Probe(c *source.Stream) media.Prober {
	return proberFunc(func(context.Context) (media.ProbeInfo, media.Reach, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.asked = append(s.asked, c.URL.String())
		info, ok := s.answers[c.URL.String()]
		if !ok {
			return media.ProbeInfo{}, media.ReachUnproven, fmt.Errorf("no scripted measurement for %s", c.URL)
		}
		return *info, media.ReachOpened, nil
	})
}

type proberFunc func(context.Context) (media.ProbeInfo, media.Reach, error)

func (f proberFunc) Probe(ctx context.Context) (media.ProbeInfo, media.Reach, error) { return f(ctx) }

// playable is a measurement of a real title: both tracks, a feature runtime.
func playable(bitRate int64, height int, runtime time.Duration) *media.ProbeInfo {
	return &media.ProbeInfo{
		BitRate: bitRate, Duration: runtime, ContentType: media.HLS,
		VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC, VideoHeight: height,
	}
}

func candidateAt(t *testing.T, contentType, raw string) *source.Stream {
	t.Helper()
	return &source.Stream{URL: sourcetest.URL(t, raw), ContentType: contentType}
}

// probed is the candidate as measureAll leaves it; a nil info is a link nobody opened.
func probed(c *source.Stream, reach media.Reach, info *media.ProbeInfo) measured {
	c.Probe = info
	return measured{Stream: c, reach: reach}
}

func TestAdmissions(t *testing.T) {
	hlsAt := func(raw string) *source.Stream { return candidateAt(t, media.HLS, raw) }
	hls := func(raw string) measured { return measured{Stream: hlsAt(raw)} }
	slideshow := &media.ProbeInfo{
		BitRate: 50_000_000, Duration: 2 * time.Hour, ContentType: media.HLS,
		VideoCodec: media.CodecMJPEG, VideoHeight: 360, VideoHeights: []int{360}, AudioCodec: media.CodecAAC,
	}
	initSegment := playable(9_000_000, 1080, 0)
	initSegment.ContentType = media.MP4

	for _, tc := range []struct {
		name           string
		c              measured
		wantReason     reason
		wantAdmit      bool
		wantLastResort bool
	}{
		{"the origin refused it", probed(hlsAt("http://a.example/spent.m3u8"), media.ReachRefused, nil), reasonRefused, false, false},
		{"unmeasured and unrefused is a last resort", hls("http://a.example/unset.m3u8"), reasonUnproven, true, true},
		{"a browser handle", hls("blob:https://play.example/17147e13"), reasonBrowserInternal, false, false},
		{"a slideshow is not a program", probed(hlsAt("http://a.example/s.m3u8"), media.ReachOpened, slideshow), reasonNoProgram, false, false},
		{"a short runtime is an ad", probed(hlsAt("http://a.example/ad.m3u8"), media.ReachOpened, playable(9_000_000, 720, 90*time.Second)), reasonTooShort, false, false},
		{"an unstated runtime is not a short one", probed(hlsAt("http://a.example/live.m3u8"), media.ReachOpened, playable(3_000_000, 1080, 0)), reasonCastable, true, false},
		{"an MP4 with no runtime is a fragment header", probed(hlsAt("http://a.example/init.mp4"), media.ReachOpened, initSegment), reasonFragmentHeader, true, true},
		{"measured and castable", probed(hlsAt("http://a.example/f.m3u8"), media.ReachOpened, playable(3_000_000, 1080, 2*time.Hour)), reasonCastable, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := admit(tc.c)
			if got.reason != tc.wantReason || got.admit != tc.wantAdmit || got.lastResort != tc.wantLastResort {
				t.Errorf("admit = (%q, %v, last resort %v), want (%q, %v, %v)",
					got.reason, got.admit, got.lastResort, tc.wantReason, tc.wantAdmit, tc.wantLastResort)
			}
		})
	}
}

// The per-host cap spends its measurements on the document that promises a ladder, whatever its capture order.
func TestRankMeasuresTheLadderBeforeTheCapDropsIt(t *testing.T) {
	answers := map[string]*media.ProbeInfo{}
	var captured []*source.Stream
	for i := range maxProbePerHost + 1 {
		raw := fmt.Sprintf("http://a.example/v%d.m3u8", i)
		answers[raw] = playable(1_000_000, 720, 2*time.Hour)
		c := candidateAt(t, media.HLS, raw)
		c.Ladder = source.LadderSole
		captured = append(captured, c)
	}
	const master = "http://a.example/index.m3u8"
	answers[master] = playable(0, 1080, 2*time.Hour)
	ladder := candidateAt(t, media.HLS, master)
	ladder.Ladder = source.LadderMultivariant
	captured = append(captured, ladder)

	measurer := &scripted{answers: answers}
	order, err := New(testConfig, 1080, measurer.Probe).Rank(t.Context(), captured)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if asked := measurer.asked; len(asked) != maxProbePerHost || !slices.Contains(asked, master) {
		t.Fatalf("measured %v, want %d links on one host including the master", asked, maxProbePerHost)
	}
	if order[0].URL.String() != master || order[0].Ladder != source.LadderMultivariant {
		t.Errorf("best = %s (ladder %v), want the master carrying its ladder", order[0].URL, order[0].Ladder)
	}
}

func TestRankedPicksTheBestCandidate(t *testing.T) {
	at := func(contentType, path string, height int, bw int64) *source.Stream {
		return &source.Stream{URL: &url.URL{Path: path}, ContentType: contentType, Probe: &media.ProbeInfo{VideoHeight: height, BitRate: bw}}
	}
	direct := func(path string, height int, bw int64) *source.Stream { return at(media.MP4, path, height, bw) }
	hls := func(path string, height int, bw int64) *source.Stream { return at(media.HLS, path, height, bw) }
	withLadder := func(c *source.Stream, l source.Ladder) *source.Stream { c.Ladder = l; return c }
	unmeasured := &source.Stream{URL: &url.URL{Path: "/dead"}, ContentType: media.MP4, LastResort: true}

	for _, tt := range []struct {
		name      string
		pool      []*source.Stream
		maxHeight media.HeightCap
		want      string
	}{
		{"in-cap beats over-cap despite lower bitrate", []*source.Stream{direct("/4k", 2160, 20_000_000), direct("/1080", 1080, 6_000_000)}, 1080, "/1080"},
		{"a master is exempt from the cap", []*source.Stream{withLadder(hls("/master", 2160, 20_000_000), source.LadderMultivariant), direct("/1080", 1080, 6_000_000)}, 1080, "/master"},
		{"a sole rendition is bound by the cap", []*source.Stream{withLadder(hls("/v2160", 2160, 20_000_000), source.LadderSole), direct("/1080", 1080, 6_000_000)}, 1080, "/1080"},
		{"an unread playlist keeps the exemption", []*source.Stream{hls("/unread", 2160, 20_000_000), direct("/1080", 1080, 6_000_000)}, 1080, "/unread"},
		{"all over cap falls back to the tallest", []*source.Stream{direct("/4k", 2160, 20_000_000), direct("/1440", 1440, 10_000_000)}, 1080, "/4k"},
		{"height beats bitrate", []*source.Stream{hls("/recording", 800, 462), hls("/release", 1600, 0)}, 1080, "/release"},
		{"equal heights fall to the higher bitrate", []*source.Stream{hls("/thin", 1080, 800_000), hls("/rich", 1080, 6_000_000)}, 1080, "/rich"},
		{"a last resort loses to anything measured", []*source.Stream{unmeasured, direct("/4k", 2160, 20_000_000)}, 1080, "/4k"},
		{"a confirmed ladder outranks a document with none", []*source.Stream{withLadder(hls("/master", 0, 0), source.LadderMultivariant), hls("/variant", 1080, 6_000_000)}, 1080, "/master"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ranked(tt.pool, tt.maxHeight)[0].URL.Path; got != tt.want {
				t.Errorf("ranked head = %q, want %q", got, tt.want)
			}
		})
	}
}

// The comparator once was cyclic; every arrival order must rank the same.
func TestRankedIsIndependentOfArrivalOrder(t *testing.T) {
	candidate := func(path string, height int, bandwidth int64) *source.Stream {
		return &source.Stream{URL: &url.URL{Path: path}, ContentType: media.HLS, Probe: &media.ProbeInfo{VideoHeight: height, BitRate: bandwidth}}
	}
	items := []*source.Stream{candidate("/1080", 1080, 0), candidate("/unknown", 0, 500_000), candidate("/720", 720, 800_000)}
	want := []string{"/1080", "/720", "/unknown"}
	for _, p := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		order := ranked([]*source.Stream{items[p[0]], items[p[1]], items[p[2]]}, 1080)
		if got := []string{order[0].URL.Path, order[1].URL.Path, order[2].URL.Path}; !slices.Equal(got, want) {
			t.Errorf("arrival %v ranked as %v, want %v", p, got, want)
		}
	}
}
