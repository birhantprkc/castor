package source

import (
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/sourcetest"
)

var testConfig = Config{MaxHeight: 1080, ProbeMaxConcurrency: 2}

func measured(info *media.ProbeInfo) sourcetest.Answer {
	return sourcetest.Answer{Info: info, Reach: media.ReachOpened}
}

func newTestRanker(m *sourcetest.Measurer) *Ranker {
	return NewRanker(testConfig, func(c *Candidate) media.Prober { return m.Probe(c.URL) })
}

// playable is a measurement of a real title: both tracks, a feature runtime.
func playable(bitRate int64, height int, runtime time.Duration) *media.ProbeInfo {
	return &media.ProbeInfo{
		BitRate: bitRate, Duration: runtime, ContentType: media.HLS,
		VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC, VideoHeight: height,
	}
}

func candidateAt(t *testing.T, contentType, raw string) *Candidate {
	t.Helper()
	return &Candidate{URL: sourcetest.URL(t, raw), ContentType: contentType}
}

// probed is the candidate as measureAll leaves it; a nil info is a link nobody opened.
func probed(c *Candidate, reach media.Reach, info *media.ProbeInfo) *Candidate {
	c.reach, c.Probe = reach, info
	return c
}

func TestAdmissions(t *testing.T) {
	hls := func(raw string) *Candidate { return candidateAt(t, media.HLS, raw) }
	slideshow := &media.ProbeInfo{
		BitRate: 50_000_000, Duration: 2 * time.Hour, ContentType: media.HLS,
		VideoCodec: media.CodecMJPEG, VideoHeight: 360, VideoHeights: []int{360}, AudioCodec: media.CodecAAC,
	}
	initSegment := playable(9_000_000, 1080, 0)
	initSegment.ContentType = media.MP4

	for _, tc := range []struct {
		name           string
		c              *Candidate
		wantReason     reason
		wantAdmit      bool
		wantLastResort bool
	}{
		{"the origin refused it", probed(hls("http://a.example/spent.m3u8"), media.ReachRefused, nil), reasonRefused, false, false},
		{"unmeasured and unrefused is a last resort", hls("http://a.example/unset.m3u8"), reasonUnproven, true, true},
		{"a browser handle", hls("blob:https://play.example/17147e13"), reasonBrowserInternal, false, false},
		{"a slideshow is not a program", probed(hls("http://a.example/s.m3u8"), media.ReachOpened, slideshow), reasonNoProgram, false, false},
		{"a short runtime is an ad", probed(hls("http://a.example/ad.m3u8"), media.ReachOpened, playable(9_000_000, 720, 90*time.Second)), reasonTooShort, false, false},
		{"an unstated runtime is not a short one", probed(hls("http://a.example/live.m3u8"), media.ReachOpened, playable(3_000_000, 1080, 0)), reasonCastable, true, false},
		{"an MP4 with no runtime is a fragment header", probed(hls("http://a.example/init.mp4"), media.ReachOpened, initSegment), reasonFragmentHeader, true, true},
		{"measured and castable", probed(hls("http://a.example/f.m3u8"), media.ReachOpened, playable(3_000_000, 1080, 2*time.Hour)), reasonCastable, true, false},
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
func TestRankStreamsMeasuresTheLadderBeforeTheCapDropsIt(t *testing.T) {
	answers := map[string]sourcetest.Answer{}
	var captured []*Candidate
	for i := range maxProbePerHost + 1 {
		raw := fmt.Sprintf("http://a.example/v%d.m3u8", i)
		answers[raw] = measured(playable(1_000_000, 720, 2*time.Hour))
		c := candidateAt(t, media.HLS, raw)
		c.Ladder = LadderSole
		captured = append(captured, c)
	}
	const master = "http://a.example/index.m3u8"
	answers[master] = measured(playable(0, 1080, 2*time.Hour))
	ladder := candidateAt(t, media.HLS, master)
	ladder.Ladder = LadderMultivariant
	captured = append(captured, ladder)

	measurer := &sourcetest.Measurer{Answers: answers}
	order, err := newTestRanker(measurer).RankStreams(t.Context(), captured)
	if err != nil {
		t.Fatalf("RankStreams: %v", err)
	}
	if asked := measurer.Asked(); len(asked) != maxProbePerHost || !slices.Contains(asked, master) {
		t.Fatalf("measured %v, want %d links on one host including the master", asked, maxProbePerHost)
	}
	if order[0].URL.String() != master || order[0].Ladder != LadderMultivariant {
		t.Errorf("best = %s (ladder %v), want the master carrying its ladder", order[0].URL, order[0].Ladder)
	}
}

// The ranker drops a three-minute clip; Measure admits the operator's own link.
func TestMeasureDoesNotConvictALinkTheOperatorNamed(t *testing.T) {
	const link = "http://a.example/clip.mp4"
	answers := map[string]sourcetest.Answer{link: measured(playable(9_000_000, 720, 3*time.Minute))}

	if _, err := newTestRanker(&sourcetest.Measurer{Answers: answers}).
		RankStreams(t.Context(), []*Candidate{candidateAt(t, media.MP4, link)}); err == nil {
		t.Fatal("the ranker admitted a three-minute candidate, so this contrast proves nothing")
	}
	one, err := newTestRanker(&sourcetest.Measurer{Answers: answers}).Measure(t.Context(), candidateAt(t, media.MP4, link))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if one.Probe == nil || one.Bitrate() != 9_000_000 || one.LastResort {
		t.Errorf("Measure = probe %+v last resort %v, want the measured 9 Mbit/s link", one.Probe, one.LastResort)
	}
}

func TestRankedPicksTheBestCandidate(t *testing.T) {
	at := func(contentType, path string, height int, bw int64) *Candidate {
		return &Candidate{URL: &url.URL{Path: path}, ContentType: contentType, Probe: &media.ProbeInfo{VideoHeight: height, BitRate: bw}}
	}
	direct := func(path string, height int, bw int64) *Candidate { return at(media.MP4, path, height, bw) }
	hls := func(path string, height int, bw int64) *Candidate { return at(media.HLS, path, height, bw) }
	withLadder := func(c *Candidate, l Ladder) *Candidate { c.Ladder = l; return c }
	unmeasured := &Candidate{URL: &url.URL{Path: "/dead"}, ContentType: media.MP4, LastResort: true}

	for _, tt := range []struct {
		name      string
		pool      []*Candidate
		maxHeight media.HeightCap
		want      string
	}{
		{"in-cap beats over-cap despite lower bitrate", []*Candidate{direct("/4k", 2160, 20_000_000), direct("/1080", 1080, 6_000_000)}, 1080, "/1080"},
		{"a master is exempt from the cap", []*Candidate{withLadder(hls("/master", 2160, 20_000_000), LadderMultivariant), direct("/1080", 1080, 6_000_000)}, 1080, "/master"},
		{"a sole rendition is bound by the cap", []*Candidate{withLadder(hls("/v2160", 2160, 20_000_000), LadderSole), direct("/1080", 1080, 6_000_000)}, 1080, "/1080"},
		{"an unread playlist keeps the exemption", []*Candidate{hls("/unread", 2160, 20_000_000), direct("/1080", 1080, 6_000_000)}, 1080, "/unread"},
		{"all over cap falls back to the tallest", []*Candidate{direct("/4k", 2160, 20_000_000), direct("/1440", 1440, 10_000_000)}, 1080, "/4k"},
		{"height beats bitrate", []*Candidate{hls("/recording", 800, 462), hls("/release", 1600, 0)}, 1080, "/release"},
		{"equal heights fall to the higher bitrate", []*Candidate{hls("/thin", 1080, 800_000), hls("/rich", 1080, 6_000_000)}, 1080, "/rich"},
		{"a last resort loses to anything measured", []*Candidate{unmeasured, direct("/4k", 2160, 20_000_000)}, 1080, "/4k"},
		{"a confirmed ladder outranks a document with none", []*Candidate{withLadder(hls("/master", 0, 0), LadderMultivariant), hls("/variant", 1080, 6_000_000)}, 1080, "/master"},
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
	candidate := func(path string, height int, bandwidth int64) *Candidate {
		return &Candidate{URL: &url.URL{Path: path}, ContentType: media.HLS, Probe: &media.ProbeInfo{VideoHeight: height, BitRate: bandwidth}}
	}
	items := []*Candidate{candidate("/1080", 1080, 0), candidate("/unknown", 0, 500_000), candidate("/720", 720, 800_000)}
	want := []string{"/1080", "/720", "/unknown"}
	for _, p := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		order := ranked([]*Candidate{items[p[0]], items[p[1]], items[p[2]]}, 1080)
		if got := []string{order[0].URL.Path, order[1].URL.Path, order[2].URL.Path}; !slices.Equal(got, want) {
			t.Errorf("arrival %v ranked as %v, want %v", p, got, want)
		}
	}
}
