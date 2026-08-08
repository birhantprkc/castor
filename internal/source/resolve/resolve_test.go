package resolve

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// mediaPlaylist is the document a source's own URL usually turns out to be: a
// closed media playlist, which reduces to the single synthetic variant and reports
// itself VOD. Resolution over it changes nothing about the stream, which is what
// makes it the right fixture for the facts established after selection.
const mediaPlaylist = "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nseg0.ts\n#EXT-X-ENDLIST\n"

// TestResolveMarksLenientOnlySource is the fact that keeps a disguised source off
// a renderer: what castor could only open by relaxing its reader's checks is
// served locally instead of handed over. The measurement itself is ffprobe's
// verdict (see ./ffprobe); the rule that reads it is here, and it is the rule that
// decides whether a device is pointed at a URL it will refuse in silence.
func TestResolveMarksLenientOnlySource(t *testing.T) {
	for _, tc := range []struct {
		name              string
		opensUnaided      bool
		wantNeedsLeniency bool
		wantFetchable     bool
	}{
		{"a source a plain reader opens keeps its pass-through", true, false, true},
		{"a source only castor's relaxed reader opens is served instead", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const raw = "http://a.example/stream.m3u8"
			measurer := &fakeMeasurer{answers: map[string]answer{raw: {unaided: tc.opensUnaided}}}
			resolver := newTestResolver(measurer, &fakePlaylists{body: mediaPlaylist, status: http.StatusOK})

			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			resolved, _, _, err := resolver.Resolve(t.Context(), &media.Stream{URL: u, ContentType: media.HLS})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if resolved.NeedsLeniency != tc.wantNeedsLeniency {
				t.Errorf("NeedsLeniency = %v, want %v", resolved.NeedsLeniency, tc.wantNeedsLeniency)
			}
			if resolved.SelfFetchable() != tc.wantFetchable {
				t.Errorf("SelfFetchable = %v, want %v", resolved.SelfFetchable(), tc.wantFetchable)
			}
		})
	}
}

// TestResolveSkipsTheProbeItCannotUse guards the cost of that measurement. A
// header-gated source is served whatever the conformance probe would say, so
// spending one is spending a request against a single-use signed URL to confirm a
// decision already made.
func TestResolveSkipsTheProbeItCannotUse(t *testing.T) {
	const raw = "http://a.example/gated.m3u8"
	measurer := &fakeMeasurer{answers: map[string]answer{raw: {unaided: false}}}
	resolver := newTestResolver(measurer, &fakePlaylists{body: mediaPlaylist, status: http.StatusOK})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, _, err := resolver.Resolve(t.Context(), &media.Stream{
		URL:         u,
		ContentType: media.HLS,
		Headers:     http.Header{"Referer": {"https://player.example/"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := measurer.conformanceProbes(); len(got) != 0 {
		t.Errorf("the conformance probe ran for %v; a header-gated source is served regardless", got)
	}
	if resolved.NeedsLeniency {
		t.Error("NeedsLeniency was set without the probe that establishes it")
	}
}

// TestResolveKeepsTheStreamWhenThePlaylistIsRefused pins the posture on a document
// castor cannot read: selection is skipped and the source is attempted whole. It is
// deliberately not an error here, because the reader that follows carries headers
// and reconnect policy this fetch does not.
func TestResolveKeepsTheStreamWhenThePlaylistIsRefused(t *testing.T) {
	const raw = "http://a.example/spent.m3u8"
	measurer := &fakeMeasurer{answers: map[string]answer{raw: {unaided: true}}}
	playlists := &fakePlaylists{status: http.StatusForbidden, err: fmt.Errorf("fetching playlist: HTTP 403")}
	resolver := newTestResolver(measurer, playlists)

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, _, err := resolver.Resolve(t.Context(), &media.Stream{URL: u, ContentType: media.HLS})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := resolved.URL.String(); got != raw {
		t.Errorf("URL = %s, want the original %s", got, raw)
	}
	if resolved.AudioURL != nil {
		t.Errorf("AudioURL = %s, want none: no document was read to pair one from", resolved.AudioURL)
	}
}

// TestResolveIdentifiesAnUnnamedSource covers the one measurement resolution
// cannot skip: a caller that could not say what the source is (a URL with no
// telling extension) gets its container and its liveness from a probe, and a probe
// that fails aborts resolution rather than guessing.
func TestResolveIdentifiesAnUnnamedSource(t *testing.T) {
	const raw = "http://a.example/movie"

	t.Run("a measured container names the source", func(t *testing.T) {
		measurer := &fakeMeasurer{answers: map[string]answer{
			raw: {info: &media.StreamInfo{ContentType: media.MP4, Duration: 2 * time.Hour, HasVideo: true, HasAudio: true}},
		}}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		resolved, origin, _, err := newTestResolver(measurer, &fakePlaylists{}).Resolve(t.Context(), &media.Stream{URL: u})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.ContentType != media.MP4 {
			t.Errorf("ContentType = %q, want %q", resolved.ContentType, media.MP4)
		}
		if resolved.Live {
			t.Error("Live = true for a source with a known duration")
		}
		// The measurement was spent naming the container, so its duration is free. It is
		// also the only runtime a whole file has: there is no document to state one.
		if origin.Duration != 2*time.Hour {
			t.Errorf("Origin.Duration = %s, want the measured 2h: the probe is already paid for", origin.Duration)
		}
	})

	t.Run("an unmeasurable source stops resolution", func(t *testing.T) {
		measurer := &fakeMeasurer{answers: map[string]answer{raw: {err: fmt.Errorf("ffprobe: exit status 1")}}}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := newTestResolver(measurer, &fakePlaylists{}).Resolve(t.Context(), &media.Stream{URL: u}); err == nil {
			t.Fatal("resolution must fail when the source cannot be identified at all")
		}
	})
}

// fixturePlaylists serves the testdata documents the way an origin does: one
// document per path, resolved from the URL, and 404 for anything with no fixture. It
// records what it was asked for, so "the chosen rendition's playlist was fetched and
// the other two were not" is provable rather than inferred, which matters for a stage
// whose whole cost is the number of requests it makes.
type fixturePlaylists struct {
	mu      sync.Mutex
	fetched []string
}

func (f *fixturePlaylists) Fetch(_ context.Context, u *url.URL, _ http.Header) (string, int, error) {
	name := path.Base(u.Path)
	f.mu.Lock()
	f.fetched = append(f.fetched, name)
	f.mu.Unlock()

	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		return "", http.StatusNotFound, fmt.Errorf("fetching playlist: %w", err)
	}
	return string(body), http.StatusOK, nil
}

func (f *fixturePlaylists) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.fetched)
}

// resolveFixture resolves a testdata document as an HLS source under the 1080 cap.
// The container is declared, as it is for every ranked candidate, so no measurement
// is spent identifying it.
func resolveFixture(t *testing.T, name string) (*media.Stream, media.Origin, *fixturePlaylists) {
	t.Helper()
	stream, origin, _, playlists := resolveFixtureRung(t, name)
	return stream, origin, playlists
}

// resolveFixtureRung is the same, for the cases that care which rung was chosen.
func resolveFixtureRung(t *testing.T, name string) (*media.Stream, media.Origin, media.Rendition, *fixturePlaylists) {
	t.Helper()
	u, err := url.Parse("http://a.example/" + name)
	if err != nil {
		t.Fatal(err)
	}
	playlists := &fixturePlaylists{}
	stream, origin, chosen, err := newTestResolver(&fakeMeasurer{}, playlists).Resolve(t.Context(), &media.Stream{URL: u, ContentType: media.HLS})
	if err != nil {
		t.Fatalf("Resolve(%s): %v", name, err)
	}
	return stream, origin, chosen, playlists
}

// TestResolvePublishesTheLadderAndTheSegmentFacts is the stage's reason to exist: the
// choice the source offered, and what it said about its own segments, both survive
// the selection made from them. Before this, selection was the only thing that
// outlived the document.
func TestResolvePublishesTheLadderAndTheSegmentFacts(t *testing.T) {
	t.Run("a master's chosen rendition supplies the segment facts", func(t *testing.T) {
		stream, origin, playlists := resolveFixture(t, "master_ladder.m3u8")

		if got := stream.URL.String(); got != "http://a.example/ladder_1080.m3u8" {
			t.Errorf("chosen rendition = %s, want the 1080 rung under the 1080 cap", got)
		}
		if origin.Sole() {
			t.Error("Sole() = true for a three-rung ladder")
		}
		want := []media.Rendition{
			{Bitrate: 1_400_000, Height: 480},
			{Bitrate: 6_200_000, Height: 1080},
			{Bitrate: 17_000_000, Height: 2160},
		}
		if len(origin.Renditions) != len(want) {
			t.Fatalf("Renditions = %v, want %d rungs in publication order", origin.Renditions, len(want))
		}
		for i, w := range want {
			if got := origin.Renditions[i]; got.Bitrate != w.Bitrate || got.Height != w.Height {
				t.Errorf("rung %d = %d bit/s at %dp, want %d bit/s at %dp (the declared BANDWIDTH must survive)",
					i, got.Bitrate, got.Height, w.Bitrate, w.Height)
			}
		}
		// EXT-X-MAP on the chosen rendition: fMP4 fragments, which is the fact a fragile
		// read policy keys on and which no master states.
		if origin.Framing != media.FramingOutOfBand {
			t.Errorf("Framing = %s, want out-of-band: the chosen rendition declares EXT-X-MAP", origin.Framing)
		}
		if origin.Duration != 18*time.Second {
			t.Errorf("Duration = %s, want the 18s EXTINF sum", origin.Duration)
		}
		if !origin.Segmented || origin.Live || origin.Encrypted {
			t.Errorf("segmented=%v live=%v encrypted=%v, want a closed unencrypted segmented source",
				origin.Segmented, origin.Live, origin.Encrypted)
		}
		// Two documents, never four: the master, then the one rendition castor chose. The
		// rungs it did not choose have no fixture, so fetching them would have failed.
		if got := playlists.asked(); !slices.Equal(got, []string{"master_ladder.m3u8", "ladder_1080.m3u8"}) {
			t.Errorf("fetched %v, want the master then the chosen rendition alone", got)
		}
	})

	t.Run("a media playlist offered nothing to choose from", func(t *testing.T) {
		stream, origin, playlists := resolveFixture(t, "media_vod.m3u8")

		if !origin.Sole() {
			t.Errorf("Sole() = false for a media playlist; the synthetic single entry is a shape, not a choice (%v)", origin.Renditions)
		}
		if got := stream.URL.String(); got != "http://a.example/media_vod.m3u8" {
			t.Errorf("URL = %s, want the document itself: it is the thing to read", got)
		}
		// The EXTINF sum, which is the runtime ffprobe reports for a playlist as "none"
		// and which is then read as "live".
		if origin.Duration != 34500*time.Millisecond {
			t.Errorf("Duration = %s, want the 34.5s EXTINF sum", origin.Duration)
		}
		if origin.Framing != media.FramingInBand {
			t.Errorf("Framing = %s, want in-band: no EXT-X-MAP means self-contained MPEG-TS segments", origin.Framing)
		}
		if got := playlists.asked(); len(got) != 1 {
			t.Errorf("fetched %v, want one document: there is no variant to descend into", got)
		}
	})

	t.Run("an encrypted playlist says so", func(t *testing.T) {
		_, origin, _ := resolveFixture(t, "media_encrypted.m3u8")
		if !origin.Encrypted {
			t.Error("Encrypted = false for a document declaring EXT-X-KEY METHOD=AES-128")
		}
	})
}

// TestResolveStatesWhichRungItChose covers the one fact this narrowing used to spend and
// throw away. Nothing downstream can reconstruct it: the choice rewrites the stream's URL
// with the variant's own, and matching that URL back against the ladder afterwards would be
// the cast layer guessing at a statement only the source layer is in a position to make.
//
// It is what arms a degrade. The ceiling a lighter rung is chosen under is the declared rate
// of the rung being READ times the speed the reader achieved on it, so an attempt that
// cannot name its rung has no ceiling and must decline (see attempt.DegradeRendition, where
// an undeclared rate is refused as evidence rather than read as zero).
func TestResolveStatesWhichRungItChose(t *testing.T) {
	t.Run("a master states the rung it narrowed to", func(t *testing.T) {
		stream, _, chosen, _ := resolveFixtureRung(t, "master_ladder.m3u8")

		if chosen.Height != 1080 || chosen.Bitrate != 6_200_000 {
			t.Errorf("chosen rung = %d bit/s at %dp, want the 1080 rung's own declared 6200000 bit/s",
				chosen.Bitrate, chosen.Height)
		}
		if chosen.URL == nil || chosen.URL.String() != stream.URL.String() {
			t.Errorf("chosen rung URL = %v, want the URL the cast will read (%s)", chosen.URL, stream.URL)
		}
	})

	t.Run("a source with no ladder states no rung", func(t *testing.T) {
		// A media playlist offered nothing, so there is nothing to say about which rung was
		// taken. Zero is the absence of evidence here, which is exactly how a recovery has to
		// read it: a rung with no declared rate is arithmetically below every ceiling.
		_, _, chosen, _ := resolveFixtureRung(t, "media_vod.m3u8")
		if chosen.Bitrate != 0 {
			t.Errorf("chosen rung = %d bit/s, want none declared for a media playlist", chosen.Bitrate)
		}
	})

	t.Run("a document castor could not read states no rung", func(t *testing.T) {
		u, err := url.Parse("http://a.example/gone.m3u8")
		if err != nil {
			t.Fatal(err)
		}
		_, _, chosen, err := newTestResolver(&fakeMeasurer{}, &fakePlaylists{err: fmt.Errorf("403")}).
			Resolve(t.Context(), &media.Stream{URL: u, ContentType: media.HLS})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if chosen.URL != nil || chosen.Bitrate != 0 {
			t.Errorf("chosen rung = %+v, want none: the source is being attempted whole", chosen)
		}
	})
}

// TestResolveSaysWhenTheSourceOfferedNothingUnderTheCap is the observed failure in
// miniature. The cap was 1080, the source published exactly one rendition at
// 3840x1600, and castor read 17 Mbit/s of it to serve a 2 Mbit/s re-encode: the
// selection was right, and everything about the run was consistent with a cap that
// had been applied. Both facts that were missing are now on the Origin.
func TestResolveSaysWhenTheSourceOfferedNothingUnderTheCap(t *testing.T) {
	stream, origin, _ := resolveFixture(t, "master_sole.m3u8")

	if got := stream.URL.String(); got != "http://a.example/sole_1600.m3u8" {
		t.Errorf("chosen rendition = %s, want the only one on offer", got)
	}
	if !origin.Sole() {
		t.Errorf("Sole() = false, want true: one rendition is not a choice (%v)", origin.Renditions)
	}
	rung := origin.Renditions[0]
	if rung.Height != 1600 || rung.Bitrate != 17_000_000 {
		t.Errorf("rung = %d bit/s at %dp, want 17000000 bit/s at 1600p: what the cap could not reach must stay visible",
			rung.Bitrate, rung.Height)
	}
	if origin.Framing != media.FramingInBand {
		t.Errorf("Framing = %s, want in-band: the chosen rendition declares no EXT-X-MAP", origin.Framing)
	}
	if origin.Duration != 30*time.Second {
		t.Errorf("Duration = %s, want the 30s EXTINF sum", origin.Duration)
	}
}

// TestResolveKeepsTheCastWhenTheChosenRenditionIsUnreadable guards the cost of the
// second GET this phase spends on a master. The facts it buys are worth one request;
// they are not worth a cast. A rendition document castor cannot read leaves the facts
// unknown, the chosen URL selected, and resolution successful, which is the same
// posture as an unreadable master.
func TestResolveKeepsTheCastWhenTheChosenRenditionIsUnreadable(t *testing.T) {
	stream, origin, playlists := resolveFixture(t, "master_absent.m3u8")

	if got := stream.URL.String(); got != "http://a.example/gone.m3u8" {
		t.Errorf("URL = %s, want the chosen rendition: the selection stands", got)
	}
	if len(origin.Renditions) != 1 {
		t.Errorf("Renditions = %v, want the ladder read from the master", origin.Renditions)
	}
	if origin.Framing != media.FramingUnknown {
		t.Errorf("Framing = %s, want unknown: no document stated it, and unknown must not read as in-band", origin.Framing)
	}
	if origin.Duration != 0 {
		t.Errorf("Duration = %s, want 0: nothing published one", origin.Duration)
	}
	if got := playlists.asked(); len(got) != 2 {
		t.Errorf("fetched %v, want the master and one attempt at the rendition", got)
	}
}

// TestResolveSurvivesAMasterOfferingNothingCastable pins the one guard standing
// between a published document shape and a panic. A master whose only entry is
// #EXT-X-I-FRAME-STREAM-INF is multivariant with an EMPTY variant list, and the
// selection below it reduces to slices.MinFunc over an empty slice, which does not
// return an unhelpful answer, it panics. Trick-play-only masters are genuinely
// published, so the honest response is the same one an unreadable document gets: keep
// the source as it was and attempt it whole.
func TestResolveSurvivesAMasterOfferingNothingCastable(t *testing.T) {
	stream, origin, playlists := resolveFixture(t, "master_iframe_only.m3u8")

	if got := stream.URL.String(); got != "http://a.example/master_iframe_only.m3u8" {
		t.Errorf("URL = %s, want the original: there was no rendition to narrow to", got)
	}
	if len(origin.Renditions) != 0 {
		t.Errorf("Renditions = %v, want none: an I-frame playlist is not one", origin.Renditions)
	}
	if !origin.Sole() {
		t.Error("Sole() = false, want true: a source offering nothing castable offered no choice")
	}
	if got := playlists.asked(); len(got) != 1 {
		t.Errorf("fetched %v, want only the master: there was no rendition document to read", got)
	}
}

// TestParsePlaylistHarvestsWhatTheDocumentStates walks the five facts one plain GET
// yields, all of them tags the pinned decoder already parses, plus the two shapes
// where the obvious reading is wrong: METHOD=NONE is a document turning encryption
// OFF, and a sliding window's EXTINF sum is the length of the window rather than of
// the program.
func TestParsePlaylistHarvestsWhatTheDocumentStates(t *testing.T) {
	const segments = "#EXTINF:6.000,\nseg0\n#EXTINF:6.000,\nseg1\n#EXT-X-ENDLIST\n"

	for _, tc := range []struct {
		name          string
		body          string
		wantFraming   media.Framing
		wantDuration  time.Duration
		wantLive      bool
		wantEncrypted bool
		wantMulti     bool
	}{
		{
			name:         "EXT-X-MAP means fMP4 fragments",
			body:         "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n" + segments,
			wantFraming:  media.FramingOutOfBand,
			wantDuration: 12 * time.Second,
		},
		{
			name:         "no EXT-X-MAP means self-contained MPEG-TS segments",
			body:         "#EXTM3U\n" + segments,
			wantFraming:  media.FramingInBand,
			wantDuration: 12 * time.Second,
		},
		{
			name:          "EXT-X-KEY records encryption",
			body:          "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n" + segments,
			wantFraming:   media.FramingInBand,
			wantDuration:  12 * time.Second,
			wantEncrypted: true,
		},
		{
			// The tag is present and says the opposite: METHOD=NONE is how a document
			// declares that what follows is in the clear again.
			name:         "METHOD=NONE is not encryption",
			body:         "#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n" + segments,
			wantFraming:  media.FramingInBand,
			wantDuration: 12 * time.Second,
		},
		{
			// An ad break spliced into an encrypted title: a clear head, an encrypted tail.
			// The decoder hoists the FIRST key onto the playlist, and with no leading tag
			// that first key is the encrypted one, so the playlist key alone already answers
			// this. Kept because it is the shape a reader expects to be the hard one; the
			// row below is the one that actually needs the per-segment walk.
			name:          "a key acquired mid-document counts",
			body:          "#EXTM3U\n#EXTINF:6.000,\nseg0\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k\"\n#EXTINF:6.000,\nseg1\n#EXT-X-ENDLIST\n",
			wantFraming:   media.FramingInBand,
			wantDuration:  12 * time.Second,
			wantEncrypted: true,
		},
		{
			// The one shape the per-segment walk earns its keep on. An explicit METHOD=NONE
			// ahead of the opening segments is the FIRST key, so it is the one hoisted onto
			// the playlist, and reading that alone calls this source unencrypted while seg1
			// needs a key. Deleting the walk makes this row, and only this row, fail.
			name:          "a document declaring itself clear and then encrypting counts",
			body:          "#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:6.000,\nseg0\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:6.000,\nseg1\n#EXT-X-ENDLIST\n",
			wantFraming:   media.FramingInBand,
			wantDuration:  12 * time.Second,
			wantEncrypted: true,
		},
		{
			name:        "a sliding window is live and states no runtime",
			body:        "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000,\nseg0\n#EXTINF:6.000,\nseg1\n",
			wantFraming: media.FramingInBand,
			wantLive:    true,
		},
		{
			// A master lists no segments, so it states nothing about them. Unknown framing
			// here is the honest answer and the reason FramingUnknown is not in-band.
			name:        "a master states nothing about segments",
			body:        "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480\n480.m3u8\n",
			wantFraming: media.FramingUnknown,
			wantMulti:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse("http://a.example/doc.m3u8")
			if err != nil {
				t.Fatal(err)
			}
			doc, err := parsePlaylist(tc.body, u)
			if err != nil {
				t.Fatalf("parsePlaylist: %v", err)
			}
			if doc.Framing != tc.wantFraming {
				t.Errorf("Framing = %s, want %s", doc.Framing, tc.wantFraming)
			}
			if doc.Duration != tc.wantDuration {
				t.Errorf("Duration = %s, want %s", doc.Duration, tc.wantDuration)
			}
			if doc.Live != tc.wantLive {
				t.Errorf("Live = %v, want %v", doc.Live, tc.wantLive)
			}
			if doc.Encrypted != tc.wantEncrypted {
				t.Errorf("Encrypted = %v, want %v", doc.Encrypted, tc.wantEncrypted)
			}
			if doc.Multivariant != tc.wantMulti {
				t.Errorf("Multivariant = %v, want %v (this is what Origin.Sole reads)", doc.Multivariant, tc.wantMulti)
			}
		})
	}
}

// TestLadderExcludesAnAudioOnlyRung is the other half of pickVariant's audio-only
// rule. A rung nothing can be cast from is not a rung to fall back to, so it must not
// appear in what the source is reported as having offered either.
func TestLadderExcludesAnAudioOnlyRung(t *testing.T) {
	body := "#EXTM3U\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=560000,RESOLUTION=320x240,CODECS="avc1.42c00c,mp4a.40.2"` + "\nvideo.m3u8\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=1400000,CODECS="mp4a.40.2"` + "\naudio.m3u8\n"
	u, err := url.Parse("http://a.example/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parsePlaylist(body, u)
	if err != nil {
		t.Fatal(err)
	}

	rungs := ladder(doc.Variants)
	if len(rungs) != 1 || rungs[0].Height != 240 {
		t.Fatalf("ladder = %v, want the video rung alone", rungs)
	}
	// One castable rung means the source gave no choice, which is the truth: the
	// alternative was audio with no picture.
	if !(media.Origin{Renditions: rungs}).Sole() {
		t.Error("Sole() = false, want true: an audio-only rung is not an alternative")
	}
}

func TestPickVariant(t *testing.T) {
	u := func(path string) *url.URL { return &url.URL{Path: path} }
	variants := []hlsVariant{
		{URL: u("/480"), Bandwidth: 1_000_000, Height: 480, HasVideo: true},
		{URL: u("/720"), Bandwidth: 3_000_000, Height: 720, HasVideo: true},
		{URL: u("/1080"), Bandwidth: 6_000_000, Height: 1080, HasVideo: true},
		{URL: u("/2160"), Bandwidth: 20_000_000, Height: 2160, HasVideo: true},
	}
	tests := []struct {
		name      string
		maxHeight int
		want      string
	}{
		{"cap 1080 takes the 1080 variant", 1080, "/1080"},
		{"cap 720 takes the 720 variant", 720, "/720"},
		{"cap 2160 takes the 4K variant", 2160, "/2160"},
		{"cap below every variant takes the smallest", 240, "/480"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickVariant(variants, tt.maxHeight).URL.Path; got != tt.want {
				t.Errorf("pickVariant(max=%d) = %q, want %q", tt.maxHeight, got, tt.want)
			}
		})
	}
}

func TestPickVariantUnknownHeightIsEligible(t *testing.T) {
	u := func(path string) *url.URL { return &url.URL{Path: path} }
	// A variant with no RESOLUTION tag (Height 0) is trusted and wins on bandwidth
	// rather than being excluded by the cap.
	variants := []hlsVariant{
		{URL: u("/tagged"), Bandwidth: 1_000_000, Height: 1080, HasVideo: true},
		{URL: u("/untagged"), Bandwidth: 5_000_000, Height: 0, HasVideo: true},
	}
	if got := pickVariant(variants, 1080).URL.Path; got != "/untagged" {
		t.Errorf("pickVariant = %q, want /untagged (unknown height, higher bandwidth)", got)
	}
}

func TestParsePlaylist(t *testing.T) {
	body := "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480\n480.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080\n1080.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=20000000,RESOLUTION=3840x2160\n2160.m3u8\n"
	u, err := url.Parse("http://example.com/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	master, err := parsePlaylist(body, u)
	if err != nil {
		t.Fatal(err)
	}
	if master.Live {
		t.Error("master playlist should not report live")
	}
	want := []int{480, 1080, 2160}
	if len(master.Variants) != len(want) {
		t.Fatalf("got %d variants, want %d", len(master.Variants), len(want))
	}
	for i, v := range master.Variants {
		if v.Height != want[i] {
			t.Errorf("variant %d height = %d, want %d (RESOLUTION not parsed)", i, v.Height, want[i])
		}
	}
	// The cap steers selection end to end.
	if got := pickVariant(master.Variants, 1080).URL.Path; got != "/1080.m3u8" {
		t.Errorf("pickVariant(1080) = %q, want /1080.m3u8", got)
	}
}

// TestParsePlaylistDemuxedMaster is the shape that silently loses audio: the
// variants carry video only and the audio is a rendition of its own. Resolving
// the master must keep the pair together, or the cast plays silence and the
// puller dies mapping an audio track that is not in the variant it was handed.
func TestParsePlaylistDemuxedMaster(t *testing.T) {
	body := "#EXTM3U\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",DEFAULT=YES,URI="audio/eng.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",URI="subs/eng.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480,CODECS="avc1.4d401f,mp4a.40.2",AUDIO="aud"` + "\n480.m3u8\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud"` + "\n1080.m3u8\n"
	u, err := url.Parse("http://example.com/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}

	master, err := parsePlaylist(body, u)
	if err != nil {
		t.Fatal(err)
	}
	if len(master.Variants) != 2 {
		t.Fatalf("got %d variants, want 2 (a CODECS list holds the same comma that separates attributes)", len(master.Variants))
	}
	variant := pickVariant(master.Variants, 1080)
	if got := variant.URL.Path; got != "/1080.m3u8" {
		t.Errorf("pickVariant = %q, want /1080.m3u8", got)
	}
	audio := master.AudioFor(variant)
	if audio == nil {
		t.Fatal("the chosen variant's audio rendition was dropped: the cast would have no sound")
	}
	if got := audio.Path; got != "/audio/eng.m3u8" {
		t.Errorf("audio rendition = %q, want /audio/eng.m3u8", got)
	}
	// Only audio is read as a second input today; a subtitle rendition is not.
	if _, ok := master.Audio["subs"]; ok {
		t.Error("a SUBTITLES rendition must not register as an audio group")
	}
}

// TestParsePlaylistMuxedRenditionStaysSingleInput guards the other half: an
// EXT-X-MEDIA entry with no URI means the audio is inside the variant already,
// so nothing extra must be read.
func TestParsePlaylistMuxedRenditionStaysSingleInput(t *testing.T) {
	body := "#EXTM3U\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",DEFAULT=YES` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480,AUDIO="aud"` + "\n480.m3u8\n"
	u, _ := url.Parse("http://example.com/master.m3u8")

	master, err := parsePlaylist(body, u)
	if err != nil {
		t.Fatal(err)
	}
	if got := master.AudioFor(pickVariant(master.Variants, 1080)); got != nil {
		t.Errorf("a rendition without a URI is muxed into the variant; got a second input %q", got)
	}
}

// TestParsePlaylistRenditionAfterVariants covers declaration order, which a
// master is free to choose: groups declared under the variants that use them
// must still be found. The decoder hangs renditions off a neighbouring variant,
// so the group map is built from every variant rather than from the first.
func TestParsePlaylistRenditionAfterVariants(t *testing.T) {
	body := "#EXTM3U\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud"` + "\n1080.m3u8\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",DEFAULT=YES,URI="audio/eng.m3u8"` + "\n"
	u, _ := url.Parse("http://example.com/master.m3u8")

	master, err := parsePlaylist(body, u)
	if err != nil {
		t.Fatal(err)
	}
	audio := master.AudioFor(pickVariant(master.Variants, 1080))
	if audio == nil {
		t.Fatal("a rendition declared after its variant was dropped: the cast would have no sound")
	}
	if got := audio.Path; got != "/audio/eng.m3u8" {
		t.Errorf("audio rendition = %q, want /audio/eng.m3u8", got)
	}
}

// TestPickVariantSkipsAudioOnly covers the inverse failure: a master that lists
// its audio rendition as a variant of its own. It has no RESOLUTION to exclude it
// by the cap and can out-bandwidth the video variants, so on bandwidth alone it
// would win and the cast would die mapping a video track that is not there.
func TestPickVariantSkipsAudioOnly(t *testing.T) {
	body := "#EXTM3U\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=560000,RESOLUTION=320x240,CODECS="avc1.42c00c,mp4a.40.2",AUDIO="aud"` + "\n" +
		"video.m3u8\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=1400000,CODECS="mp4a.40.2",AUDIO="aud"` + "\naudio.m3u8\n"
	u, _ := url.Parse("http://example.com/master.m3u8")

	master, err := parsePlaylist(body, u)
	if err != nil {
		t.Fatal(err)
	}
	if got := pickVariant(master.Variants, 1080).URL.Path; got != "/video.m3u8" {
		t.Errorf("pickVariant = %q, want /video.m3u8 (an audio-only variant is not castable)", got)
	}
}

// TestRankedPicksTheBestCandidate pins the comparator's tier order, which is what
// decides whether a cast is pointed at a link nobody could open: a candidate within
// the height cap outranks one above it, bandwidth then height break the rest, and an
// unmeasured candidate sits below every measured one however little it advertises.
func TestRankedPicksTheBestCandidate(t *testing.T) {
	direct := func(path string, height int, bw int64) candidate {
		return candidate{stream: &media.Stream{URL: &url.URL{Path: path}, Bandwidth: bw, ContentType: media.MP4}, height: height}
	}
	hls := func(path string, height int, bw int64) candidate {
		return candidate{stream: &media.Stream{URL: &url.URL{Path: path}, Bandwidth: bw, ContentType: media.HLS}, height: height}
	}
	// unmeasured is the candidate admitted with nothing behind it: no height, and
	// whatever bandwidth extraction supplied, which in practice is none.
	unmeasured := func(path string) candidate {
		return candidate{stream: &media.Stream{URL: &url.URL{Path: path}, ContentType: media.MP4}, lastResort: true}
	}

	tests := []struct {
		name      string
		pool      []candidate
		maxHeight int
		want      string
	}{
		{
			name:      "in-cap direct beats over-cap direct despite lower bitrate",
			pool:      []candidate{direct("/4k", 2160, 20_000_000), direct("/1080", 1080, 6_000_000)},
			maxHeight: 1080,
			want:      "/1080",
		},
		{
			name:      "HLS master is exempt from the cap and wins on bandwidth",
			pool:      []candidate{hls("/master", 2160, 20_000_000), direct("/1080", 1080, 6_000_000)},
			maxHeight: 1080,
			want:      "/master",
		},
		{
			name:      "all over cap falls back to the tallest",
			pool:      []candidate{direct("/4k", 2160, 20_000_000), direct("/1440", 1440, 10_000_000)},
			maxHeight: 1080,
			want:      "/4k",
		},
		{
			// A height ffprobe declined to report is unknown, not short. With nothing
			// to compare on resolution the pair falls through to bandwidth, so a
			// candidate is never punished for being hard to measure.
			name:      "unknown height is eligible",
			pool:      []candidate{direct("/unknown", 0, 20_000_000), direct("/1080", 1080, 6_000_000)},
			maxHeight: 1080,
			want:      "/unknown",
		},
		{
			// The field failure this ordering exists for. ffprobe cannot report a
			// top-level bit_rate for an HLS master, so a proper release arrives floored
			// to bandwidth 1, while a plain recording it CAN measure arrives with a real
			// number. With bitrate compared first, 462 beat 1 and the 1600-line picture
			// lost to an 800-line one without its resolution ever being looked at.
			name:      "a floored master outranks a measured but shorter candidate",
			pool:      []candidate{hls("/recording", 800, 462), hls("/release", 1600, 1)},
			maxHeight: 1080,
			want:      "/release",
		},
		{
			// The other half of that rule: resolution decides, and bitrate still breaks
			// a genuine tie, so a cleaner encode at the same height is not thrown away.
			name:      "equal heights still fall to the higher bitrate",
			pool:      []candidate{hls("/thin", 1080, 800_000), hls("/rich", 1080, 6_000_000)},
			maxHeight: 1080,
			want:      "/rich",
		},
		{
			name:      "tied bandwidth (unprobeable HLS master bit_rate) falls back to tallest height",
			pool:      []candidate{hls("/290", 290, 1), hls("/1808", 1808, 1), hls("/580", 580, 1)},
			maxHeight: 2160,
			want:      "/1808",
		},
		{
			// The pick that cost a real cast, in miniature: the unmeasured candidate has
			// height 0, height 0 is inside every cap, and the within-cap tier used to be
			// compared before anything else.
			name:      "an unmeasured last resort loses to a measured candidate that exceeds the cap",
			pool:      []candidate{unmeasured("/dead"), direct("/4k", 2160, 20_000_000)},
			maxHeight: 1080,
			want:      "/4k",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ranked(tt.pool, tt.maxHeight)[0].stream.URL.Path; got != tt.want {
				t.Errorf("ranked head = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRefetchLeavesTheLinkItWasGivenAlone is the whole reason the cast layer reaches this
// package through a type of its own rather than calling Resolve.
//
// Resolve rewrites the stream it is handed, which is its job: a resolved stream is what
// castor reads. The links a cast walks are the ranker's ordering, held for the life of the
// cast as the record of what was published and what was tried, so resolving one in place
// would narrow a master to a rung inside that record and leave nothing able to say what the
// candidate had been.
func TestRefetchLeavesTheLinkItWasGivenAlone(t *testing.T) {
	u, err := url.Parse("http://a.example/master_ladder.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	link := &media.Stream{URL: u, ContentType: media.HLS, Headers: http.Header{"Referer": {"https://player.example/"}}}

	resolved, origin, chosen, err := NewPrograms(newTestResolver(&fakeMeasurer{}, &fixturePlaylists{})).
		Refetch(t.Context(), link)
	if err != nil {
		t.Fatalf("Refetch: %v", err)
	}

	if link.URL.String() != "http://a.example/master_ladder.m3u8" {
		t.Errorf("the link handed over now reads %s, so the ordering no longer records what was published", link.URL)
	}
	if resolved == link {
		t.Error("Refetch answered with the very stream it was given, so any later resolution rewrites the ordering")
	}
	if got := resolved.URL.String(); got != "http://a.example/ladder_1080.m3u8" {
		t.Errorf("resolved URL = %s, want the rung narrowed to under the cap", got)
	}
	if resolved.Headers.Get("Referer") == "" {
		t.Error("the resolved copy lost the headers the link only answers to")
	}
	if len(origin.Renditions) != 3 || chosen.Height != 1080 {
		t.Errorf("Refetch published %d renditions and chose %dp, want the whole ladder and the 1080 rung",
			len(origin.Renditions), chosen.Height)
	}
}
