package resolve

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// This file tests what castor is willing to play, which had no test at all until
// the measurement it ranks on became a port: probing was an unexported exec call, so
// every rule was reachable only through a real ffprobe against a real origin, the
// whole file skipped on a machine without one, and the interesting shapes (a decoy
// that out-bandwidths the feature, a link nobody answered, a spent signed URL) were
// never constructed. Behind the Measurer port they are table rows.

// fakeMeasurer answers from a script keyed by URL and records what it was asked.
// It is the whole point of the port: no subprocess, no origin, and a measurement
// that can be any shape the field produces, including ones no fixture generator
// would emit (an HLS master whose bit_rate ffprobe cannot report, a link that
// answers nothing at all).
type fakeMeasurer struct {
	answers map[string]answer

	mu       sync.Mutex
	measured []string
	unaided  []string
}

// answer is one scripted measurement. A nil info with an err is the shape the
// production prober returns for a link it never opened, and the rules must read it
// as "nothing is known", not "this source carries nothing". The reach is scripted
// beside it because the admission of a failed measurement turns entirely on it.
type answer struct {
	info    *media.StreamInfo
	reach   media.Reach
	err     error
	unaided bool
}

// measured is the ordinary answer: the origin served the source and ffprobe read
// it.
func measured(info *media.StreamInfo) answer {
	return answer{info: info, reach: media.ReachOpened}
}

// refusedByOrigin is a spent signed link. The origin answered, and answered no, so
// there is nothing the puller's reconnects can rescue.
func refusedByOrigin() answer {
	return answer{reach: media.ReachRefused, err: fmt.Errorf("ffprobe: exit status 1\nServer returned 403 Forbidden (access denied)")}
}

// probeKilled is the measurement castor abandoned at its own deadline: nothing was
// learned and the origin never refused anything, which is the one failure that is
// still worth attempting.
func probeKilled() answer {
	return answer{err: fmt.Errorf("ffprobe: signal: killed (measurement budget 30s)")}
}

func (f *fakeMeasurer) Measure(_ context.Context, s *media.Stream) (*media.StreamInfo, media.Reach, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.measured = append(f.measured, s.URL.String())
	a, ok := f.answers[s.URL.String()]
	if !ok {
		return nil, media.ReachUnproven, fmt.Errorf("no scripted measurement for %s", s.URL)
	}
	return a.info, a.reach, a.err
}

func (f *fakeMeasurer) OpensUnaided(_ context.Context, s *media.Stream) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unaided = append(f.unaided, s.URL.String())
	return f.answers[s.URL.String()].unaided
}

func (f *fakeMeasurer) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.measured)
}

func (f *fakeMeasurer) conformanceProbes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.unaided)
}

// fakePlaylists serves one fixture document to every fetch, and records the URLs
// it was asked for so a skipped fetch is provable rather than inferred.
type fakePlaylists struct {
	body    string
	status  int
	err     error
	fetched []string
}

func (f *fakePlaylists) Fetch(_ context.Context, u *url.URL, _ http.Header) (string, int, error) {
	f.fetched = append(f.fetched, u.String())
	return f.body, f.status, f.err
}

// newTestResolver wires the policy to scripted ports under a 1080 cap. The
// concurrency is 2 because that is the shipping default, so the ranker under test
// is the one that runs.
func newTestResolver(m Measurer, p Playlists) *Resolver {
	return New(Config{MaxHeight: 1080, ProbeMaxConcurrency: 2}, m, p)
}

// playable is a measurement of a real title: both tracks, a feature runtime.
func playable(bitRate int64, height int) *media.StreamInfo {
	return &media.StreamInfo{
		BitRate:     bitRate,
		Duration:    2 * time.Hour,
		ContentType: media.HLS,
		HasVideo:    true,
		HasAudio:    true,
		VideoHeight: height,
	}
}

func streams(t *testing.T, raws ...string) []*media.Stream {
	t.Helper()
	return contentStreams(t, media.HLS, raws...)
}

// contentStreams builds candidates of a given container, which matters to ranking
// for one reason: the height cap binds a direct file and exempts an HLS master (see
// candidate.exceedsCap).
func contentStreams(t *testing.T, contentType string, raws ...string) []*media.Stream {
	t.Helper()
	out := make([]*media.Stream, len(raws))
	for i, raw := range raws {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = &media.Stream{URL: u, ContentType: contentType}
	}
	return out
}

// TestAdmissions walks every row of the table over the shapes it exists for, and
// pins the two properties the table's correctness rests on: the bottom row matches
// everything (so admit's shape-naming default is unreachable while the table is
// intact), and a measurement whose reach nobody established is admitted rather than
// dropped. The second is why media.ReachUnproven is the zero value: silence must
// never be able to convict a candidate.
func TestAdmissions(t *testing.T) {
	// A pre-roll is a perfectly castable program in every respect but its runtime,
	// which is the whole reason its row keys on the duration alone.
	ad := playable(9_000_000, 720)
	ad.Duration = 90 * time.Second

	for _, tc := range []struct {
		name           string
		m              measurement
		wantReason     reason
		wantAdmit      bool
		wantLastResort bool
	}{
		{
			name:       "the origin refused it",
			m:          measurement{reach: media.ReachRefused},
			wantReason: reasonRefused,
		},
		{
			name:           "nothing was measured and nobody refused it",
			m:              measurement{reach: media.ReachUnproven},
			wantReason:     reasonUnproven,
			wantAdmit:      true,
			wantLastResort: true,
		},
		{
			name:           "a reach nobody set is still admitted",
			m:              measurement{},
			wantReason:     reasonUnproven,
			wantAdmit:      true,
			wantLastResort: true,
		},
		{
			name:       "measured, but no audio",
			m:          measurement{reach: media.ReachOpened, info: &media.StreamInfo{HasVideo: true, VideoHeight: 1080, Duration: 2 * time.Hour}},
			wantReason: reasonNoProgram,
		},
		{
			name:       "measured, but shorter than any real title",
			m:          measurement{reach: media.ReachOpened, info: ad},
			wantReason: reasonTooShort,
		},
		{
			name:       "measured and castable",
			m:          measurement{reach: media.ReachOpened, info: playable(3_000_000, 1080)},
			wantReason: reasonCastable,
			wantAdmit:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := admit(tc.m)
			if got.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.reason, tc.wantReason)
			}
			if got.admit != tc.wantAdmit {
				t.Errorf("admit = %v, want %v", got.admit, tc.wantAdmit)
			}
			if got.lastResort != tc.wantLastResort {
				t.Errorf("lastResort = %v, want %v", got.lastResort, tc.wantLastResort)
			}
		})
	}

	t.Run("the last row is total", func(t *testing.T) {
		last := admissions[len(admissions)-1]
		if !last.when(measurement{info: playable(1, 1)}) || !last.admit {
			t.Error("the bottom row must admit every shape reaching it, or admit's default arm becomes a way to refuse a castable stream")
		}
	})
}

// TestRankStreamsDropsDecoysHard is the pathology aggregators actually serve: the
// image-only playlist, the video-without-audio one and the pre-roll ad all carry
// far more bandwidth than the feature, so anything short of dropping them outright
// hands the cast an ad or a slideshow.
func TestRankStreamsDropsDecoysHard(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		// An image playlist: ffprobe reports a "video" track that decodes to a still,
		// which the prober refuses to count, so nothing playable is left.
		"http://a.example/slideshow.m3u8": measured(&media.StreamInfo{BitRate: 50_000_000, Duration: 2 * time.Hour, HasAudio: true}),
		"http://a.example/silent.m3u8":    measured(&media.StreamInfo{BitRate: 40_000_000, Duration: 2 * time.Hour, HasVideo: true, VideoHeight: 1080}),
		"http://a.example/preroll.m3u8":   measured(&media.StreamInfo{BitRate: 30_000_000, Duration: 90 * time.Second, HasVideo: true, HasAudio: true, VideoHeight: 1080}),
		"http://a.example/feature.m3u8":   measured(playable(3_000_000, 1080)),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	order, err := resolver.RankStreams(t.Context(), streams(t,
		"http://a.example/slideshow.m3u8",
		"http://a.example/silent.m3u8",
		"http://a.example/preroll.m3u8",
		"http://a.example/feature.m3u8",
	))
	if err != nil {
		t.Fatalf("RankStreams: %v", err)
	}
	if got := order[0].URL.String(); got != "http://a.example/feature.m3u8" {
		t.Errorf("best = %s, want the feature; a decoy won on bandwidth", got)
	}
	if len(order) != 1 {
		t.Errorf("ordering has %d entries, want only the feature: a dropped decoy must not survive as a fallback the cast can walk to", len(order))
	}
}

// TestRankStreamsFailsWhenNothingIsCastable covers the other end: with every
// candidate dropped there is nothing to fall back to, and saying so beats handing
// the cast an ad.
func TestRankStreamsFailsWhenNothingIsCastable(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/ad1.m3u8": measured(&media.StreamInfo{BitRate: 9_000_000, Duration: 30 * time.Second, HasVideo: true, HasAudio: true, VideoHeight: 720}),
		"http://a.example/ad2.m3u8": measured(&media.StreamInfo{BitRate: 8_000_000, Duration: 15 * time.Second, HasVideo: true, HasAudio: true, VideoHeight: 720}),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	_, err := resolver.RankStreams(t.Context(), streams(t, "http://a.example/ad1.m3u8", "http://a.example/ad2.m3u8"))
	if err == nil {
		t.Fatal("a pool of nothing but ads must fail ranking")
	}
	if !strings.Contains(err.Error(), "no castable stream") {
		t.Errorf("error = %q, want it to say nothing was castable", err)
	}
}

// TestRankStreamsNeedsCandidates pins the empty case: extraction found nothing, so
// there is no ranking to do and no pool-empty message to confuse it with.
func TestRankStreamsNeedsCandidates(t *testing.T) {
	resolver := newTestResolver(&fakeMeasurer{}, &fakePlaylists{})
	if _, err := resolver.RankStreams(t.Context(), nil); err == nil {
		t.Fatal("ranking no streams must fail")
	}
}

// TestRankStreamsDropsARefusedCandidate pins that a refusal is an answer and not a
// gap in castor's knowledge: a link whose origin answered 403 used to be kept
// at bandwidth 0 as a "transient" failure, and with every other candidate a dropped
// decoy it was then selected and cast against for minutes. A refusal is an answer,
// it is the same answer every reader gets, and the only thing left to do with it is
// say so.
func TestRankStreamsDropsARefusedCandidate(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/spent.m3u8": refusedByOrigin(),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	_, err := resolver.RankStreams(t.Context(), streams(t, "http://a.example/spent.m3u8"))
	if err == nil {
		t.Fatal("a candidate the origin refused must not be cast")
	}
	if !strings.Contains(err.Error(), string(reasonRefused)) {
		t.Errorf("error = %q, want the refusal tallied by reason so the user knows to extract again", err)
	}
}

// TestRankStreamsTalliesRejectionsByReason is what the failure has to say to be
// actionable. "no castable stream" alone reads as broken extraction; the shapes read
// as expired links, an aggregator's decoys, or an ad pod, which are three different
// next moves for the user.
func TestRankStreamsTalliesRejectionsByReason(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/spent.m3u8":   refusedByOrigin(),
		"http://a.example/silent.m3u8":  measured(&media.StreamInfo{BitRate: 40_000_000, Duration: 2 * time.Hour, HasVideo: true, VideoHeight: 1080}),
		"http://a.example/preroll.m3u8": measured(&media.StreamInfo{BitRate: 30_000_000, Duration: 90 * time.Second, HasVideo: true, HasAudio: true, VideoHeight: 1080}),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	_, err := resolver.RankStreams(t.Context(), streams(t,
		"http://a.example/spent.m3u8", "http://a.example/silent.m3u8", "http://a.example/preroll.m3u8"))
	if err == nil {
		t.Fatal("nothing was admitted, so ranking must fail")
	}
	for _, want := range []string{
		"1 " + string(reasonRefused),
		"1 " + string(reasonNoProgram),
		"1 " + string(reasonTooShort),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to count %q", err, want)
		}
	}
}

// TestRankStreamsKeepsAnUnprovenCandidateWhenEverythingElseIsADecoy is the
// combination nothing covered: a measurement castor abandoned at its own deadline,
// competing against nothing but hard-rejected decoys. This is the exact pool from
// the failing run, and the answer is deliberately different from the refused case
// above: the origin never said no, the puller reconnects where ffprobe gave up, so
// attempting it beats refusing to cast anything.
func TestRankStreamsKeepsAnUnprovenCandidateWhenEverythingElseIsADecoy(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/unproven.m3u8":  probeKilled(),
		"http://a.example/slideshow.m3u8": measured(&media.StreamInfo{BitRate: 50_000_000, Duration: 2 * time.Hour, HasAudio: true}),
		"http://a.example/preroll.m3u8":   measured(&media.StreamInfo{BitRate: 30_000_000, Duration: 90 * time.Second, HasVideo: true, HasAudio: true, VideoHeight: 1080}),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	order, err := resolver.RankStreams(t.Context(), streams(t,
		"http://a.example/unproven.m3u8", "http://a.example/slideshow.m3u8", "http://a.example/preroll.m3u8"))
	if err != nil {
		t.Fatalf("RankStreams: %v", err)
	}
	if len(order) != 1 || order[0].URL.String() != "http://a.example/unproven.m3u8" {
		t.Fatalf("ordering = %v, want the unproven candidate alone: a killed probe is not a refusal", order)
	}
}

// TestRankStreamsRanksAnUnprovenCandidateBelowEveryMeasuredOne pins the half of the
// last-resort rule that makes it defensible, including the case that used to invert
// it. The measured candidate here is a direct 2160p under a 1080 cap, so it EXCEEDS
// the cap, while the unproven one has height 0 and no cap can exclude it: comparing
// the within-cap tier before anything else is exactly how a link nobody could open
// used to beat a real 20 Mbit/s source.
func TestRankStreamsRanksAnUnprovenCandidateBelowEveryMeasuredOne(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/unproven.mp4": probeKilled(),
		"http://a.example/2160.mp4":     measured(playable(20_000_000, 2160)),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	order, err := resolver.RankStreams(t.Context(), contentStreams(t, media.MP4,
		"http://a.example/unproven.mp4", "http://a.example/2160.mp4"))
	if err != nil {
		t.Fatalf("RankStreams: %v", err)
	}
	if got := order[0].URL.String(); got != "http://a.example/2160.mp4" {
		t.Errorf("best = %s, want the measured 2160p even though it exceeds the cap", got)
	}
	if len(order) != 2 || order[1].URL.String() != "http://a.example/unproven.mp4" {
		t.Errorf("ordering = %v, want the unproven candidate kept as the tail", order)
	}
}

// TestRankStreamsFloorsMeasuredBandwidth covers the case the field produces for
// nearly every HLS master: ffprobe reports no top-level bit_rate, so every such
// candidate is floored to the same bandwidth and the tie has to be broken on
// measured height. Without the floor a measured master would be indistinguishable
// from a candidate carrying no measurement at all.
func TestRankStreamsFloorsMeasuredBandwidth(t *testing.T) {
	short, tall := playable(0, 580), playable(0, 1808)
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/580.m3u8":  measured(short),
		"http://a.example/1808.m3u8": measured(tall),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	order, err := resolver.RankStreams(t.Context(), streams(t, "http://a.example/580.m3u8", "http://a.example/1808.m3u8"))
	if err != nil {
		t.Fatalf("RankStreams: %v", err)
	}
	best := order[0]
	if got := best.URL.String(); got != "http://a.example/1808.m3u8" {
		t.Errorf("best = %s, want the taller master (the bandwidth tie must break on height)", got)
	}
	if best.Bandwidth != 1 {
		t.Errorf("Bandwidth = %d, want the floor of 1: a measured master must not rank as unmeasured", best.Bandwidth)
	}
}

// TestRankStreamsStopsProbingOneHost guards the courtesy that keeps a whole
// ranking alive: an embed proxy publishes a master plus a tail of variant
// playlists behind one signature, and probing the lot answers 429 to everything
// after the first few. Candidates arrive master-first, so the cap keeps the head of
// the list and never asks about the tail.
func TestRankStreamsStopsProbingOneHost(t *testing.T) {
	answers := map[string]answer{"http://b.example/other.m3u8": measured(playable(1_000_000, 720))}
	var raws []string
	for i := range 7 {
		raw := fmt.Sprintf("http://a.example/v%d.m3u8", i)
		raws = append(raws, raw)
		answers[raw] = measured(playable(int64(1_000_000+i), 720))
	}
	raws = append(raws, "http://b.example/other.m3u8")

	measurer := &fakeMeasurer{answers: answers}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	if _, err := resolver.RankStreams(t.Context(), streams(t, raws...)); err != nil {
		t.Fatalf("RankStreams: %v", err)
	}

	asked := measurer.asked()
	if len(asked) != maxProbePerHost+1 {
		t.Errorf("measured %d candidates, want %d (%d per host plus the other host)", len(asked), maxProbePerHost+1, maxProbePerHost)
	}
	for _, dropped := range []string{"http://a.example/v5.m3u8", "http://a.example/v6.m3u8"} {
		if slices.Contains(asked, dropped) {
			t.Errorf("%s was probed; the per-host cap must drop the tail, not the head", dropped)
		}
	}
	if !slices.Contains(asked, "http://b.example/other.m3u8") {
		t.Error("a candidate on a second host was dropped; the cap is per host")
	}
}
