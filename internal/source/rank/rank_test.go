package rank

import (
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/sourcetest"
)

var testConfig = Config{MaxHeight: 1080, ProbeMaxConcurrency: 2}

func answered(info *media.ProbeInfo) sourcetest.Answer {
	return sourcetest.Answer{Info: info, Reach: media.ReachOpened}
}

func newTestRanker(m *sourcetest.Measurer) *Ranker {
	return New(testConfig, func(c *source.Candidate) media.Prober { return m.Probe(c.URL) })
}

// playable is a measurement of a real title: both tracks, a feature runtime.
func playable(bitRate int64, height int, runtime time.Duration) *media.ProbeInfo {
	return &media.ProbeInfo{
		BitRate: bitRate, Duration: runtime, ContentType: media.HLS,
		VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC, VideoHeight: height,
	}
}

func candidateAt(t *testing.T, contentType, raw string) *source.Candidate {
	t.Helper()
	return &source.Candidate{URL: sourcetest.URL(t, raw), ContentType: contentType}
}

// probed is the candidate as measureAll leaves it; a nil info is a link nobody opened.
func probed(c *source.Candidate, reach media.Reach, info *media.ProbeInfo) measured {
	c.Probe = info
	return measured{Candidate: c, reach: reach}
}

func TestAdmissions(t *testing.T) {
	hlsAt := func(raw string) *source.Candidate { return candidateAt(t, media.HLS, raw) }
	hls := func(raw string) measured { return measured{Candidate: hlsAt(raw)} }
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
	answers := map[string]sourcetest.Answer{}
	var captured []*source.Candidate
	for i := range maxProbePerHost + 1 {
		raw := fmt.Sprintf("http://a.example/v%d.m3u8", i)
		answers[raw] = answered(playable(1_000_000, 720, 2*time.Hour))
		c := candidateAt(t, media.HLS, raw)
		c.Ladder = source.LadderSole
		captured = append(captured, c)
	}
	const master = "http://a.example/index.m3u8"
	answers[master] = answered(playable(0, 1080, 2*time.Hour))
	ladder := candidateAt(t, media.HLS, master)
	ladder.Ladder = source.LadderMultivariant
	captured = append(captured, ladder)

	measurer := &sourcetest.Measurer{Answers: answers}
	order, err := newTestRanker(measurer).Rank(t.Context(), captured)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if asked := measurer.Asked(); len(asked) != maxProbePerHost || !slices.Contains(asked, master) {
		t.Fatalf("measured %v, want %d links on one host including the master", asked, maxProbePerHost)
	}
	if order[0].URL.String() != master || order[0].Ladder != source.LadderMultivariant {
		t.Errorf("best = %s (ladder %v), want the master carrying its ladder", order[0].URL, order[0].Ladder)
	}
}

func TestRankedPicksTheBestCandidate(t *testing.T) {
	at := func(contentType, path string, height int, bw int64) *source.Candidate {
		return &source.Candidate{URL: &url.URL{Path: path}, ContentType: contentType, Probe: &media.ProbeInfo{VideoHeight: height, BitRate: bw}}
	}
	direct := func(path string, height int, bw int64) *source.Candidate { return at(media.MP4, path, height, bw) }
	hls := func(path string, height int, bw int64) *source.Candidate { return at(media.HLS, path, height, bw) }
	withLadder := func(c *source.Candidate, l source.Ladder) *source.Candidate { c.Ladder = l; return c }
	unmeasured := &source.Candidate{URL: &url.URL{Path: "/dead"}, ContentType: media.MP4, LastResort: true}

	for _, tt := range []struct {
		name      string
		pool      []*source.Candidate
		maxHeight media.HeightCap
		want      string
	}{
		{"in-cap beats over-cap despite lower bitrate", []*source.Candidate{direct("/4k", 2160, 20_000_000), direct("/1080", 1080, 6_000_000)}, 1080, "/1080"},
		{"a master is exempt from the cap", []*source.Candidate{withLadder(hls("/master", 2160, 20_000_000), source.LadderMultivariant), direct("/1080", 1080, 6_000_000)}, 1080, "/master"},
		{"a sole rendition is bound by the cap", []*source.Candidate{withLadder(hls("/v2160", 2160, 20_000_000), source.LadderSole), direct("/1080", 1080, 6_000_000)}, 1080, "/1080"},
		{"an unread playlist keeps the exemption", []*source.Candidate{hls("/unread", 2160, 20_000_000), direct("/1080", 1080, 6_000_000)}, 1080, "/unread"},
		{"all over cap falls back to the tallest", []*source.Candidate{direct("/4k", 2160, 20_000_000), direct("/1440", 1440, 10_000_000)}, 1080, "/4k"},
		{"height beats bitrate", []*source.Candidate{hls("/recording", 800, 462), hls("/release", 1600, 0)}, 1080, "/release"},
		{"equal heights fall to the higher bitrate", []*source.Candidate{hls("/thin", 1080, 800_000), hls("/rich", 1080, 6_000_000)}, 1080, "/rich"},
		{"a last resort loses to anything measured", []*source.Candidate{unmeasured, direct("/4k", 2160, 20_000_000)}, 1080, "/4k"},
		{"a confirmed ladder outranks a document with none", []*source.Candidate{withLadder(hls("/master", 0, 0), source.LadderMultivariant), hls("/variant", 1080, 6_000_000)}, 1080, "/master"},
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
	candidate := func(path string, height int, bandwidth int64) *source.Candidate {
		return &source.Candidate{URL: &url.URL{Path: path}, ContentType: media.HLS, Probe: &media.ProbeInfo{VideoHeight: height, BitRate: bandwidth}}
	}
	items := []*source.Candidate{candidate("/1080", 1080, 0), candidate("/unknown", 0, 500_000), candidate("/720", 720, 800_000)}
	want := []string{"/1080", "/720", "/unknown"}
	for _, p := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		order := ranked([]*source.Candidate{items[p[0]], items[p[1]], items[p[2]]}, 1080)
		if got := []string{order[0].URL.Path, order[1].URL.Path, order[2].URL.Path}; !slices.Equal(got, want) {
			t.Errorf("arrival %v ranked as %v, want %v", p, got, want)
		}
	}
}
