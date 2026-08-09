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
	info    *media.ProbeInfo
	reach   media.Reach
	err     error
	unaided bool
}

// measured is the ordinary answer: the origin served the source and ffprobe read
// it.
func measured(info *media.ProbeInfo) answer {
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

// protocolNotFound is what ffprobe answers for a URL whose scheme it has no protocol
// for, before it opens a socket. It is a failed measurement exactly like probeKilled,
// and that is the whole reason the scheme has to be read: on the measurement alone the
// two are indistinguishable, and one of them is worth attempting.
func protocolNotFound(raw string) answer {
	return answer{err: fmt.Errorf("ffprobe: exit status 1\n%s: Protocol not found", raw)}
}

// blobHandle is the URL from the field run, shortened only in its UUID: a news site
// whose player fed a MediaSource, so the one thing extraction captured was the object
// URL the page had handed its own video element.
const blobHandle = "blob:https://play.tv3.lt/17147e13-0f36-4d5e-9a11-8b4c0d2e6f70"

func (f *fakeMeasurer) Measure(_ context.Context, s *media.Stream) (*media.ProbeInfo, media.Reach, error) {
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
func playable(bitRate int64, height int) *media.ProbeInfo {
	return &media.ProbeInfo{
		BitRate:     bitRate,
		Duration:    2 * time.Hour,
		ContentType: media.HLS,
		VideoCodec:  media.CodecH264,
		AudioCodec:  media.CodecAAC,
		VideoHeight: height,
	}
}

// slideshow is the decoy an aggregator serves: audio and a track that decodes to a still,
// which the one probe decoder reports as no video track at all (see media.DecodeProbe).
func slideshow(bitRate int64) *media.ProbeInfo {
	return &media.ProbeInfo{BitRate: bitRate, Duration: 2 * time.Hour, ContentType: media.HLS, AudioCodec: media.CodecAAC}
}

// silent is the other decoy: a real picture with no audio anywhere, which cannot be remuxed
// into anything a renderer plays.
func silent(bitRate int64, height int) *media.ProbeInfo {
	info := playable(bitRate, height)
	info.AudioCodec = ""
	return info
}

// preroll is a spliced-in ad: castable in every respect but its runtime, and encoded well
// above the title it interrupts.
func preroll(bitRate int64, height int, runtime time.Duration) *media.ProbeInfo {
	info := playable(bitRate, height)
	info.Duration = runtime
	return info
}

func streams(t *testing.T, raws ...string) []*media.Stream {
	t.Helper()
	return contentStreams(t, media.HLS, raws...)
}

// streamAt is one candidate, for the table tests that hand admit a measurement
// directly. Every measurement carries the candidate it was taken of, because that is
// the only shape production builds (see measureAll) and because the table's first row
// reads the URL: a measurement with no stream on it is a shape no probe can produce.
func streamAt(t *testing.T, raw string) *media.Stream {
	t.Helper()
	return contentStreams(t, media.HLS, raw)[0]
}

// contentStreams builds candidates of a given container, which matters to ranking
// for one reason: the height cap binds a direct file outright, while a playlist is
// exempt until its own tags say it advertises a single rendition (see
// measurement.exceedsCap).
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
			m:          measurement{stream: streamAt(t, "http://a.example/spent.m3u8"), reach: media.ReachRefused},
			wantReason: reasonRefused,
		},
		{
			name:           "nothing was measured and nobody refused it",
			m:              measurement{stream: streamAt(t, "http://a.example/silent-origin.m3u8"), reach: media.ReachUnproven},
			wantReason:     reasonUnproven,
			wantAdmit:      true,
			wantLastResort: true,
		},
		{
			name:           "a reach nobody set is still admitted",
			m:              measurement{stream: streamAt(t, "http://a.example/unset.m3u8")},
			wantReason:     reasonUnproven,
			wantAdmit:      true,
			wantLastResort: true,
		},
		{
			name:       "measured, but no audio",
			m:          measurement{stream: streamAt(t, "http://a.example/slideshow.m3u8"), reach: media.ReachOpened, info: silent(0, 1080)},
			wantReason: reasonNoProgram,
		},
		{
			name:       "measured, but shorter than any real title",
			m:          measurement{stream: streamAt(t, "http://a.example/preroll.m3u8"), reach: media.ReachOpened, info: ad},
			wantReason: reasonTooShort,
		},
		{
			name:       "measured and castable",
			m:          measurement{stream: streamAt(t, "http://a.example/feature.m3u8"), reach: media.ReachOpened, info: playable(3_000_000, 1080)},
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

	t.Run("a browser handle is refused before anything is read into it", func(t *testing.T) {
		// The field shape exactly: nothing was measured, nobody refused anything, and the
		// reasonUnproven row would therefore admit it as a last resort. The scheme row has
		// to win here, or the cast is aimed at a URL with no protocol behind it.
		got := admit(measurement{stream: streamAt(t, blobHandle)})
		if got.reason != reasonBrowserInternal {
			t.Errorf("reason = %q, want %q: the unproven row must not shadow a structurally uncastable URL", got.reason, reasonBrowserInternal)
		}
		if got.admit || got.lastResort {
			t.Errorf("admit = %v, lastResort = %v, want both false: there is no protocol for a reader to retry", got.admit, got.lastResort)
		}
	})

	t.Run("the total row is total", func(t *testing.T) {
		// It carries no predicate at all, which is what makes it total: a `when` here would
		// be a condition the walk never asks and a candidate it would silently refuse.
		if admissions.total.when != nil {
			t.Error("the total row carries a predicate the walk never asks: a row that can decline belongs in admissions.rules, where the walk reads it")
		}
		if !admissions.total.admit {
			t.Error("the total row refuses what reaches it, so a measured castable candidate is dropped by the row that exists to keep it")
		}
		clean := measurement{stream: streamAt(t, "https://cdn.example/master.m3u8"), info: playable(1, 1)}
		if got := admit(clean); got.reason != admissions.total.reason || !got.admit {
			t.Errorf("a measured, castable candidate earned %q (admit=%v), want the total row %q", got.reason, got.admit, admissions.total.reason)
		}
	})
}

// TestAdmissionKeysUncastabilityOnTheScheme is the half of the browser-handle rule
// that keeps it safe: what it must NOT reject. Every row here carries the same clean
// measurement, so the only thing that can separate them is the scheme, and each
// admitted row names a scheme a reader really does fetch.
//
// data: and file: are the two that make the rule a denylist rather than an allowlist.
// ffmpeg has an input protocol for both (`ffprobe data:video/mp4;base64,AAAA` gets past
// protocol lookup and fails on the payload instead, and a local file is how a hand-typed
// cast is spelled), so an allowlist of the schemes castor happens to have thought of
// would refuse a candidate that works. The path row is the pattern-guessing failure the
// same rule would have if it matched text anywhere but the scheme.
func TestAdmissionKeysUncastabilityOnTheScheme(t *testing.T) {
	for _, tc := range []struct {
		name       string
		raw        string
		wantReason reason
		wantAdmit  bool
	}{
		{
			name:       "a blob handle is not fetchable by anything",
			raw:        blobHandle,
			wantReason: reasonBrowserInternal,
		},
		{
			name:       "the scheme is the same scheme in upper case",
			raw:        "BLOB:https://play.tv3.lt/17147e13-0f36-4d5e-9a11-8b4c0d2e6f70",
			wantReason: reasonBrowserInternal,
		},
		{
			name:       "a sandboxed filesystem handle is the same kind of handle",
			raw:        "filesystem:https://play.tv3.lt/temporary/movie.mp4",
			wantReason: reasonBrowserInternal,
		},
		{
			name:       "a path that merely contains the word blob is an ordinary URL",
			raw:        "https://cdn.example/blob/movie.m3u8",
			wantReason: reasonCastable,
			wantAdmit:  true,
		},
		{
			name:       "a local file is castable, and ffmpeg has a protocol for it",
			raw:        "file:///movies/movie.mkv",
			wantReason: reasonCastable,
			wantAdmit:  true,
		},
		{
			name:       "a data URL is castable, and ffmpeg has a protocol for it too",
			raw:        "data:video/mp4;base64,AAAA",
			wantReason: reasonCastable,
			wantAdmit:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := admit(measurement{stream: streamAt(t, tc.raw), reach: media.ReachOpened, info: playable(3_000_000, 1080)})
			if got.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.reason, tc.wantReason)
			}
			if got.admit != tc.wantAdmit {
				t.Errorf("admit = %v, want %v", got.admit, tc.wantAdmit)
			}
		})
	}
}

// TestRankStreamsNeverAimsACastAtABrowserHandle drives the field pool through the real
// ranker beside a candidate that measured cleanly. The handle must be gone from the
// ORDERING and not merely beaten in it: the ordering is what a cast walks, so a handle
// kept as a tail entry is still a URL a cast attempts once the head fails, and it fails
// there for the same reason it failed here.
func TestRankStreamsNeverAimsACastAtABrowserHandle(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		blobHandle:                      protocolNotFound(blobHandle),
		"http://a.example/feature.m3u8": measured(playable(3_000_000, 1080)),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	order, err := resolver.RankStreams(t.Context(), streams(t, blobHandle, "http://a.example/feature.m3u8"))
	if err != nil {
		t.Fatalf("RankStreams: %v", err)
	}
	if len(order) != 1 || order[0].URL.String() != "http://a.example/feature.m3u8" {
		t.Fatalf("ordering = %v, want the feature alone: a browser handle must not survive as a candidate a cast can walk to", order)
	}
}

// TestRankStreamsFailsWhenEveryCaptureIsABrowserHandle is the field run itself: the
// page played through a MediaSource, so the pool was the object URL and an ad. Nothing
// is admitted, and the failure has to name the shape rather than count it as one more
// expired link. "refused by the origin" tells the user to extract again, which here
// produces the same handle a second time; this reason says the real stream was never
// captured, which is a different thing to do next.
func TestRankStreamsFailsWhenEveryCaptureIsABrowserHandle(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		blobHandle:                      protocolNotFound(blobHandle),
		"http://a.example/preroll.m3u8": measured(preroll(30_000_000, 1080, 90*time.Second)),
	}}
	resolver := newTestResolver(measurer, &fakePlaylists{})

	_, err := resolver.RankStreams(t.Context(), streams(t, blobHandle, "http://a.example/preroll.m3u8"))
	if err == nil {
		t.Fatal("a pool of nothing but a browser handle and an ad must fail ranking")
	}
	if !strings.Contains(err.Error(), "1 "+string(reasonBrowserInternal)) {
		t.Errorf("error = %q, want the handle tallied by its own reason so the user knows extraction never saw the stream", err)
	}
	if !strings.Contains(err.Error(), "1 "+string(reasonTooShort)) {
		t.Errorf("error = %q, want the ad still tallied beside it", err)
	}
}

// TestRankStreamsDropsDecoysHard is the pathology aggregators actually serve: the
// image-only playlist, the video-without-audio one and the pre-roll ad all carry
// far more bandwidth than the feature, so anything short of dropping them outright
// hands the cast an ad or a slideshow.
func TestRankStreamsDropsDecoysHard(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		// An image playlist: ffprobe reports a "video" track that decodes to a still,
		// which the prober refuses to count, so nothing playable is left.
		"http://a.example/slideshow.m3u8": measured(slideshow(50_000_000)),
		"http://a.example/silent.m3u8":    measured(silent(40_000_000, 1080)),
		"http://a.example/preroll.m3u8":   measured(preroll(30_000_000, 1080, 90*time.Second)),
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

// The ladder has to survive ranking, and it is the one fact ranking cannot recover: it
// was read out of the browser's own cache while extracting, and that session is gone.
// Ranking measures onto a COPY of every candidate, so that a rejected one leaves no
// marks on extraction's value, and a copy is exactly where a carried fact goes missing.
//
// The pick itself is the reason to care. Both candidates here are the shapes the field
// serves: a master ffprobe can report no top-level bit_rate for, arriving floored to 1
// with the height of whichever variant it opened, against a plain recording it measures
// cleanly. The master wins because it carries rungs a starving cast can drop to.
func TestRankingCarriesTheLadderItWasGiven(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/index.m3u8":     measured(playable(1, 0)),
		"http://a.example/recording.m3u8": measured(playable(6_000_000, 1080)),
	}}
	candidates := streams(t, "http://a.example/index.m3u8", "http://a.example/recording.m3u8")
	candidates[0].Ladder = media.LadderMultivariant
	candidates[1].Ladder = media.LadderSole

	order, err := newTestResolver(measurer, &fakePlaylists{}).RankStreams(t.Context(), candidates)
	if err != nil {
		t.Fatalf("RankStreams: %v", err)
	}
	if got := order[0].URL.String(); got != "http://a.example/index.m3u8" {
		t.Errorf("best = %s, want the document that advertised renditions", got)
	}
	if order[0].Ladder != media.LadderMultivariant {
		t.Errorf("the ranked stream carries renditions=%v, so nothing a recovery reads can find the ladder that was in hand", order[0].Ladder)
	}
	if order[1].Ladder != media.LadderSole {
		t.Errorf("the alternative carries renditions=%v, want the fact its own document established", order[1].Ladder)
	}
}

// TestRankingCarriesWhatItMeasured is the fact the whole cast phase reads and the ranker used
// to keep to itself. The measurement is paid for here, once, and every party that needs it runs
// after ranking is over: the composition asks the height whether this source may be handed to a
// renderer untouched, and the arithmetic that turns "this cast is slow" into a number divides
// the duration. The height was logged and dropped at the package boundary, so a measured 2160p
// file arrived at the composition as 0 and was handed to a 1080-capped renderer.
//
// The unmeasured candidate is the other half: it carries none of them, and Probed is what says
// so. Duration 0 on a link nobody opened is not a stream with no ending in it.
func TestRankingCarriesWhatItMeasured(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/feature.mp4":  measured(playable(6_000_000, 2160)),
		"http://a.example/unproven.mp4": probeKilled(),
	}}
	order, err := newTestResolver(measurer, &fakePlaylists{}).RankStreams(t.Context(),
		contentStreams(t, media.MP4, "http://a.example/feature.mp4", "http://a.example/unproven.mp4"))
	if err != nil {
		t.Fatalf("RankStreams: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("ordering = %v, want both candidates", order)
	}

	best := order[0]
	if best.Height != 2160 {
		t.Errorf("the ranked stream carries height %d, want the measured 2160: a height that stops here is a 4K source handed to a 1080-capped renderer", best.Height)
	}
	if best.Duration != 2*time.Hour {
		t.Errorf("the ranked stream carries duration %s, want the measured 2h: without it no projected runtime can be formed", best.Duration)
	}
	if !best.Probed {
		t.Error("the ranked stream does not record that it was measured, so a runtime of 0 on the next candidate reads as a live edge")
	}

	if tail := order[1]; tail.Height != 0 || tail.Duration != 0 || tail.Probed {
		t.Errorf("the unmeasured candidate carries height=%d duration=%s probed=%v, want nothing established: no probe ever opened it",
			tail.Height, tail.Duration, tail.Probed)
	}
}

// TestRankStreamsHonoursTheCeilingOnlyOnAProvenSingleRendition drives the selection
// half of the height ceiling through the real ranker. Both candidates are .m3u8 and both
// were measured cleanly, so the only thing that can separate them is what their own
// documents said about renditions.
//
// A directly captured 2160p variant playlist is the shape that cost a 1080-capped user
// 4K: it advertises no renditions, so 2160 is exactly what a cast against it reads and
// there is no rung to narrow to. Preferring the 1080p link instead costs nothing, where
// honouring the same ceiling once the 4K read is under way costs a decode, a scale and a
// realtime re-encode. A candidate whose body nobody could read keeps the exemption, so
// nothing established stays incapable of convicting a candidate.
func TestRankStreamsHonoursTheCeilingOnlyOnAProvenSingleRendition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ladder media.Ladder
		want   string
	}{
		{
			name:   "a document advertising one rendition makes its height a ceiling",
			ladder: media.LadderSole,
			want:   "http://a.example/1080.m3u8",
		},
		{
			name:   "renditions nobody could read leave the exemption in place",
			ladder: media.LadderUnknown,
			want:   "http://a.example/2160.m3u8",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			measurer := &fakeMeasurer{answers: map[string]answer{
				"http://a.example/2160.m3u8": measured(playable(20_000_000, 2160)),
				"http://a.example/1080.m3u8": measured(playable(6_000_000, 1080)),
			}}
			candidates := streams(t, "http://a.example/2160.m3u8", "http://a.example/1080.m3u8")
			candidates[0].Ladder = tc.ladder
			candidates[1].Ladder = media.LadderSole

			order, err := newTestResolver(measurer, &fakePlaylists{}).RankStreams(t.Context(), candidates)
			if err != nil {
				t.Fatalf("RankStreams: %v", err)
			}
			if got := order[0].URL.String(); got != tc.want {
				t.Errorf("best = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestRankStreamsFailsWhenNothingIsCastable covers the other end: with every
// candidate dropped there is nothing to fall back to, and saying so beats handing
// the cast an ad.
func TestRankStreamsFailsWhenNothingIsCastable(t *testing.T) {
	measurer := &fakeMeasurer{answers: map[string]answer{
		"http://a.example/ad1.m3u8": measured(preroll(9_000_000, 720, 30*time.Second)),
		"http://a.example/ad2.m3u8": measured(preroll(8_000_000, 720, 15*time.Second)),
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
		"http://a.example/silent.m3u8":  measured(silent(40_000_000, 1080)),
		"http://a.example/preroll.m3u8": measured(preroll(30_000_000, 1080, 90*time.Second)),
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
		"http://a.example/slideshow.m3u8": measured(slideshow(50_000_000)),
		"http://a.example/preroll.m3u8":   measured(preroll(30_000_000, 1080, 90*time.Second)),
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
