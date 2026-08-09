package pipeline

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/resolve"
)

// fakeTarget is the configured renderer without a network: a static profile, the
// capabilities it would negotiate, and a count of how many times it was actually connected.
// That count is the point of the fake rather than a detail of it, because the property this
// table has to keep is about a connect NOT happening.
type fakeTarget struct {
	profile  media.Renderer
	caps     media.Renderer
	err      error
	acquired atomic.Int64
}

func (t *fakeTarget) Profile() media.Renderer { return t.profile }

func (t *fakeTarget) Acquire(context.Context) (Renderer, error) {
	t.acquired.Add(1)
	if t.err != nil {
		return nil, t.err
	}
	return &fakeDevice{caps: t.caps}, nil
}

// selfFetching and pushOnly are the two static profiles a family can have, in exactly the
// shape device.Profile answers: the one fact, and nothing else measured.
func selfFetching() media.Renderer { return media.Renderer{SelfFetch: true} }
func pushOnly() media.Renderer     { return media.Renderer{} }

// TestCompositionsReproduceTheForksTheyReplaced drives the table over every shape the two
// nested booleans used to answer, and asserts both halves of each answer: which composition
// runs, and whether the renderer had to be connected to find out.
func TestCompositionsReproduceTheForksTheyReplaced(t *testing.T) {
	tests := []struct {
		name       string
		profile    media.Renderer
		caps       media.Renderer
		sourceCT   string
		headers    http.Header
		height     int
		maxHeight  int
		preference core.DeliveryPreference

		composition string
		connects    bool
	}{{
		// The push-only family: knowable from the profile alone, which is what lets the read
		// start before discovery has finished.
		name:        "a renderer that never fetches for itself is a read-once buffer, decided without connecting",
		profile:     pushOnly(),
		caps:        dlnaLike(),
		sourceCT:    media.MKV,
		composition: "read-once",
		connects:    false,
	}, {
		// And that answer does not depend on what such a renderer would have advertised: a DLNA
		// that accepts the source container still cannot fetch it.
		name:        "a push-only renderer that accepts the container is still a read-once buffer",
		profile:     pushOnly(),
		caps:        media.Renderer{Containers: []string{media.MP4}, ServedContainer: media.MPEGTS},
		sourceCT:    media.MP4,
		composition: "read-once",
		connects:    false,
	}, {
		name:        "a renderer that fetches for itself and accepts the source is handed the URL",
		profile:     selfFetching(),
		caps:        chromecastLike(media.MP4),
		sourceCT:    media.MP4,
		composition: "passthrough",
		connects:    true,
	}, {
		name:        "a renderer that rejects the source container is served a remux",
		profile:     selfFetching(),
		caps:        chromecastLike(media.MP4),
		sourceCT:    media.MKV,
		composition: "remux",
		connects:    true,
	}, {
		// A URL carries none of the headers castor captured, so a renderer handed one would
		// fetch nothing.
		name:        "a header-gated source is remuxed even for a renderer that accepts it",
		profile:     selfFetching(),
		caps:        chromecastLike(media.MP4),
		sourceCT:    media.MP4,
		headers:     http.Header{"Referer": {"https://player.example/"}},
		composition: "remux",
		connects:    true,
	}, {
		name:        "the operator's serve preference remuxes a source nothing else would",
		profile:     selfFetching(),
		caps:        chromecastLike(media.MP4),
		sourceCT:    media.MP4,
		preference:  core.DeliveryServe,
		composition: "remux",
		connects:    true,
	}, {
		// The preference has nothing to answer for a renderer that never fetches: that cast is
		// served either way, and it is still composed without connecting.
		name:        "the serve preference changes nothing for a renderer that never fetches",
		profile:     pushOnly(),
		caps:        dlnaLike(),
		sourceCT:    media.MP4,
		preference:  core.DeliveryServe,
		composition: "read-once",
		connects:    false,
	}, {
		// The row the ceiling adds, and the only one where refusing costs something: this
		// renderer fetches for itself and takes the container, so it would have played the
		// source untouched. Castor reads none of a pass-through's bytes and therefore cannot
		// scale one, so the source the operator capped at 1080 can only be kept off the
		// renderer by composing this cast as a remux, which reads the 1600-line source and
		// scales it. That leg wants a hardware encoder to hold realtime, and taking it anyway
		// is the trade: casting more than was asked for is not an option castor has.
		name:        "a source declared above the cast's ceiling is remuxed rather than handed over",
		profile:     selfFetching(),
		caps:        chromecastLike(media.HLS),
		sourceCT:    media.HLS,
		height:      1600,
		maxHeight:   1080,
		composition: "remux",
		connects:    true,
	}, {
		// The carve-out reaching the table: the identical cast whose source declared no height
		// is still the cheap leg. Most pass-throughs are this shape (a direct file, a media
		// playlist with no RESOLUTION), and refusing them on absence of evidence would cost
		// castor the composition almost entirely.
		name:        "a source that declared no height is still handed over under the same ceiling",
		profile:     selfFetching(),
		caps:        chromecastLike(media.HLS),
		sourceCT:    media.HLS,
		maxHeight:   1080,
		composition: "passthrough",
		connects:    true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := &fakeTarget{profile: tt.profile, caps: tt.caps}
			source := &media.Stream{
				URL:         &url.URL{Scheme: "https", Host: "cdn.example", Path: "/movie"},
				ContentType: tt.sourceCT,
				Headers:     tt.headers,
			}

			row, shape, err := compose(t.Context(), compositions, target, newHeld(target.Acquire), core.Shape{
				Source: source, Delivery: tt.preference, Height: tt.height, MaxHeight: tt.maxHeight,
			})
			if err != nil {
				t.Fatalf("compose: %v", err)
			}
			if row.name != tt.composition {
				t.Errorf("composition = %q, want %q (shape: %s)", row.name, tt.composition, shape)
			}
			if connected := target.acquired.Load() > 0; connected != tt.connects {
				t.Errorf("the renderer was connected = %v, want %v: this is what decides whether a single-use source URL ages behind discovery",
					connected, tt.connects)
			}
			if shape.Negotiated != tt.connects {
				t.Errorf("the shape reports negotiated = %v while the renderer was connected = %v", shape.Negotiated, tt.connects)
			}
		})
	}
}

// TestARendererServedALivePlaylistIsRemuxedIntoOne is the premise everything the segmented
// delivery says about its renderer rests on, which is why it is pinned here rather than assumed
// in core.
//
// A cast served as a live playlist is composed as a remux, and a remux has no reader of castor's
// own behind it: it hands the delivery driver no supervisor, so nothing watches its renderer
// while it plays. That is what makes the delivery's own statement at the end of the cast the ONLY
// judgement of whether anybody fetched it, and it is what a renderer accepting the URL and never
// asking for a segment used to walk straight through.
func TestARendererServedALivePlaylistIsRemuxedIntoOne(t *testing.T) {
	// A renderer that fetches for itself, cannot be handed this source (it takes no Matroska) and
	// asks to be served a live playlist. That is the shipping shape of a segmented cast.
	caps := media.Renderer{SelfFetch: true, Containers: []string{media.HLS}, ServedContainer: media.HLS}
	target := &fakeTarget{profile: selfFetching(), caps: caps}
	source := &media.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example", Path: "/movie"}, ContentType: media.MKV}

	row, shape, err := compose(t.Context(), compositions, target, newHeld(target.Acquire), core.Shape{Source: source})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if row.name != "remux" {
		t.Fatalf("composition = %q, want remux (shape: %s)", row.name, shape)
	}

	into, err := core.ServedFormat(caps)
	if err != nil {
		t.Fatal(err)
	}
	if into.Delivery != media.DeliverSegmented {
		t.Fatalf("a cast served %s is delivered by mechanism %v, want the segmented one: this test says nothing about the leg that hides an unfetched cast unless it is that mechanism it lands on",
			caps.ServedContainer, into.Delivery)
	}
}

// TestNoNegotiatedRowIsReachableWhenTheStaticRowMatched is the invariant that makes the
// two-pass resolution sound rather than a guess dressed up as a table.
//
// A row answered from the static profile is answered BEFORE a renderer exists, so if the
// renderer could then have produced a different answer, that cast would be composed for a
// renderer nobody asked: the single-use source URL is spent by then and nothing downstream
// can read it again. What makes it hold is that the profile answers one fact and leaves
// everything else zero, so no negotiated rule is even evaluable in the first pass.
func TestNoNegotiatedRowIsReachableWhenTheStaticRowMatched(t *testing.T) {
	// Capabilities that WOULD satisfy the passthrough rule outright, handed to a target whose
	// profile says the family never fetches for itself. If the passes could disagree, this is
	// the shape where it would show.
	target := &fakeTarget{profile: pushOnly(), caps: chromecastLike(media.MP4)}
	source := &media.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example"}, ContentType: media.MP4}

	row, _, err := compose(t.Context(), compositions, target, newHeld(target.Acquire), core.Shape{Source: source})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if row.name != "read-once" {
		t.Fatalf("composition = %q, want read-once: the static profile answered first", row.name)
	}
	if n := target.acquired.Load(); n != 0 {
		t.Fatalf("the renderer was connected %d time(s) though the static row had already matched", n)
	}

	// Stated the other way round, over the rows themselves: for every shape a first-pass row
	// answers, the pass that admits negotiated rows must reach the same one, or the answer
	// depended on which pass asked.
	for _, negotiatedCaps := range []media.Renderer{chromecastLike(media.MP4), dlnaLike(), {}} {
		static := core.Shape{Renderer: pushOnly(), Source: source}
		first, ok := match(compositions, static, profileOnly)
		if !ok {
			t.Fatal("no first-pass row answers a push-only profile")
		}
		full := core.Shape{Renderer: negotiatedCaps, Source: source, Negotiated: true}
		full.Renderer.SelfFetch = static.Renderer.SelfFetch // the fact the two passes share
		if second, _ := match(compositions, full, negotiated); second.name != first.name {
			t.Errorf("a shape the profile composed as %q composes as %q once connected", first.name, second.name)
		}
	}
}

// TestEveryNegotiatedRowConnectsFirst pins the two columns' agreement. A row chosen from what
// a renderer negotiated has, by definition, already connected one, so a row claiming both
// would be describing a cast that read the negotiated container of a renderer it never
// acquired.
func TestEveryNegotiatedRowConnectsFirst(t *testing.T) {
	for _, row := range compositions {
		if row.needs == negotiated && row.connect != connectFirst {
			t.Errorf("composition %q is chosen from negotiated capabilities but claims to connect %s", row.name, row.connect)
		}
	}
}

// TestAShapeNoRowAnswersIsReportedAsSuch covers the arm every other table here has: a missing
// row is an error naming the shape, never a fall-through into whichever wiring happened to be
// last. The shipped table's last row is total, so this drives a table with it removed.
func TestAShapeNoRowAnswersIsReportedAsSuch(t *testing.T) {
	partial := slices.DeleteFunc(slices.Clone(compositions), func(row composition) bool { return row.name == "remux" })

	target := &fakeTarget{profile: selfFetching(), caps: chromecastLike(media.MKV)}
	source := &media.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example"}, ContentType: media.MP4}

	_, _, err := compose(t.Context(), partial, target, newHeld(target.Acquire), core.Shape{Source: source})
	if err == nil {
		t.Fatal("a shape no row answers was composed anyway")
	}
	if !strings.Contains(err.Error(), "self_fetch=true") || !strings.Contains(err.Error(), media.MP4) {
		t.Errorf("error = %q, which does not name the shape nobody wrote a row for", err)
	}
}

// TestOnlyARendererThatNeverFetchesForItselfIsServedABuffer is where "a pass-through cannot
// burn subtitles" now lives. It used to be an axis the planner computed and forced off; it is
// structural instead, because the read-once composition is the only one that produces the
// picture (the others hand over a container) and it is the only one that builds stages.
func TestOnlyARendererThatNeverFetchesForItselfIsServedABuffer(t *testing.T) {
	source := &media.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example"}, ContentType: media.MP4}

	for _, caps := range []media.Renderer{
		chromecastLike(media.MP4),
		chromecastLike(media.MKV),
		{SelfFetch: true},
	} {
		for _, pref := range []core.DeliveryPreference{core.DeliveryAuto, core.DeliveryServe} {
			target := &fakeTarget{profile: selfFetching(), caps: caps}
			row, shape, err := compose(t.Context(), compositions, target, newHeld(target.Acquire), core.Shape{Source: source, Delivery: pref})
			if err != nil {
				t.Fatalf("compose: %v", err)
			}
			if row.name == "read-once" {
				t.Errorf("a renderer that fetches for itself was composed as a read-once buffer (shape: %s)", shape)
			}
		}
	}
}

// TestEachCompositionsPolicyIsTheOneItsPictureRequires pins the column's VALUES. It is the
// half that cannot be read off any cast's output, because a re-encode to the source's own
// codec probes exactly like a copy of it: what a wrong value here costs is a whole title
// re-encoded for nothing, or a picture handed over that the renderer cannot decode.
//
// A remux changes the wrapper and not the picture, so it hands over an envelope this renderer
// never advertised. The buffered encode is produced for a renderer that has already answered,
// so it holds it to what it said, and can be forced to produce the picture by a burn-in
// besides. Neither value says anything about the cast's height ceiling, which is not a
// judgement about a renderer and is not the policy's to lift.
func TestEachCompositionsPolicyIsTheOneItsPictureRequires(t *testing.T) {
	want := map[string]core.VideoPolicy{
		"read-once": core.CopyWhatFits,
		"remux":     core.CopyWhatever,
		// A composition that produces no encode carries no policy: there is nothing for a copy
		// rule to be asked about, and a value here would be one nobody reads.
		"passthrough": 0,
	}
	for _, row := range compositions {
		policy, stated := want[row.name]
		if !stated {
			t.Errorf("composition %q carries a copy policy nobody has said what it is for", row.name)
			continue
		}
		if row.policy != policy {
			t.Errorf("composition %q may refuse %v, want %v", row.name, row.policy, policy)
		}
	}
	if len(want) != len(compositions) {
		t.Errorf("%d compositions are shipped and %d have a stated policy", len(compositions), len(want))
	}
}

// TestTheCompositionsPolicyDecidesWhatItsCopyMayRefuse pins the column against the decision
// it feeds. The two served compositions differ here and (beyond what they measure) nowhere
// else, and the difference is exactly one clause wide: a remux changes the wrapper and not the
// picture, so it copies a bitstream this renderer never advertised, while the buffered encode
// is produced for a renderer that has answered and holds it to that answer.
func TestTheCompositionsPolicyDecidesWhatItsCopyMayRefuse(t *testing.T) {
	ffmpegPath, _ := requireFFmpegTools(t)

	// A source that turns on the capability clause and on nothing else: inside the cast's
	// ceiling, and in a codec MPEG-TS carries, so carriage refuses nothing. The renderer
	// advertises no video support at all, so what it decodes is the only open question.
	probe := media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 720, AudioCodec: media.CodecAAC, AudioChannels: 2}
	caps := media.Renderer{ServedContainer: media.MPEGTS, Audio: []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}}}
	into, err := core.ServedFormat(caps)
	if err != nil {
		t.Fatal(err)
	}

	decide := func(policy core.VideoPolicy, source media.ProbeInfo) string {
		c := &cast{
			cfg:    core.Config{Transcode: core.TranscodeConfig{FFmpegPath: ffmpegPath}, Resolver: resolve.Config{MaxHeight: 1080}},
			policy: policy,
		}
		return c.encode(t.Context(), caps, into, encodeInput{facts: core.Facts{Probe: source, Measured: true}}).Video.Name()
	}

	if got := decide(core.CopyWhatever, probe); got != "copy" {
		t.Errorf("under the remux composition's policy the video was %q, want a copy: it changes the wrapper and not the picture", got)
	}
	if got := decide(core.CopyWhatFits, probe); got == "copy" {
		t.Error("under the read-once composition's policy a source the renderer advertises no support for was copied")
	}

	// And the clause that is NOT the policy's, through the same wiring, because this is where
	// the ceiling reaches the decision FROM CONFIGURATION on both legs. The divergence it
	// closes was invisible by construction: which composition a cast lands on is settled by
	// what a renderer answered during discovery, so a ceiling honoured on one path and dropped
	// on the other meant the same max_height produced 1080p or 2160p with nothing telling a
	// user which they were getting.
	tall := media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 2160, AudioCodec: media.CodecAAC, AudioChannels: 2}
	for _, policy := range []core.VideoPolicy{core.CopyWhatFits, core.CopyWhatever} {
		if got := decide(policy, tall); got == "copy" {
			t.Errorf("under %v a 2160p source was copied under the configured 1080 ceiling", policy)
		}
	}
}

// TestTheAttemptsDecodeOrderOutranksEveryCompositionsPolicy is the other half of the
// recovery for a copy that broke, and it is the half that has to reach the ENCODE.
//
// On the buffered leg the encode reads the very packets the reader produced, so a cast that
// told its reader to decode a track and then copied it straight back out has changed
// nothing about what the renderer receives. On the remux leg the encode IS the reader, and
// its policy is the permissive one precisely because nothing is known against the
// bitstream, which is exactly the belief this evidence contradicts.
func TestTheAttemptsDecodeOrderOutranksEveryCompositionsPolicy(t *testing.T) {
	// A source every leg would otherwise copy: h264 inside the height ceiling, in a codec
	// the renderer decodes, into a container that carries it.
	probe := media.ProbeInfo{VideoCodec: media.CodecH264, VideoHeight: 720, VideoBitDepth: 8, AudioCodec: media.CodecAAC, AudioChannels: 2}
	caps := media.Renderer{
		ServedContainer: media.MPEGTS,
		Video:           []media.VideoSupport{{Codec: media.CodecH264}},
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
	}
	into, err := core.ServedFormat(caps)
	if err != nil {
		t.Fatal(err)
	}

	// No ffmpeg is needed to read this: what is asserted is that the copy was REFUSED, and
	// whichever encoder a host can prove is beside the point.
	decide := func(policy core.VideoPolicy, decode carriage.Axes) (string, string) {
		c := &cast{
			cfg:     core.Config{Resolver: resolve.Config{MaxHeight: 1080}},
			attempt: attempt.Attempt{Decode: decode},
			policy:  policy,
		}
		opts := c.encode(t.Context(), caps, into, encodeInput{facts: core.Facts{Probe: probe, Measured: true}})
		return opts.Video.Name(), opts.Audio.Name()
	}

	for _, policy := range []core.VideoPolicy{core.CopyWhatFits, core.CopyWhatever} {
		if video, audio := decide(policy, carriage.Axes{}); video != "copy" || audio != "copy" {
			t.Fatalf("under %v a source nothing is known against was encoded (%s/%s), so this case is measuring the wrong thing",
				policy, video, audio)
		}
		if video, _ := decide(policy, carriage.Axes{Video: true}); video == "copy" {
			t.Errorf("under %v the video a reader of this cast already died copying was copied again", policy)
		}
		if _, audio := decide(policy, carriage.Axes{Audio: true}); audio == "copy" {
			t.Errorf("under %v the audio a reader of this cast already died copying was copied again", policy)
		}
		// Per axis, so a blamed half does not cost the other one its quality.
		if _, audio := decide(policy, carriage.Axes{Video: true}); audio != "copy" {
			t.Errorf("under %v a blamed video axis re-encoded the audio (%s) as well", policy, audio)
		}
	}
}

// TestTheRendererIsAcquiredOnceHoweverManyPartiesAsk covers what makes the composition able to
// ask for the renderer wherever it needs one. Both the negotiated pass and the leg that plays
// ask, and a second connect is not a wasted round trip: it is a second session to a renderer
// that is already holding a URL, and on the family whose connect sideloads a channel it is a
// minute of it.
func TestTheRendererIsAcquiredOnceHoweverManyPartiesAsk(t *testing.T) {
	target := &fakeTarget{profile: selfFetching(), caps: chromecastLike(media.MP4)}
	h := newHeld(target.Acquire)

	first, err := h.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("two parties asking for the renderer were handed two different sessions")
	}
	if n := target.acquired.Load(); n != 1 {
		t.Errorf("the renderer was connected %d times, want once", n)
	}

	// A failure is remembered the same way, so a cast does not retry a connect nobody asked it
	// to retry.
	failing := &fakeTarget{profile: selfFetching(), err: errors.New("no such device on the network")}
	h = newHeld(failing.Acquire)
	if _, err := h.get(t.Context()); err == nil {
		t.Fatal("a failing connect reported success")
	}
	if _, err := h.get(t.Context()); err == nil {
		t.Fatal("a second ask was answered as if the first had worked")
	}
	if n := failing.acquired.Load(); n != 1 {
		t.Errorf("a failing connect was attempted %d times, want once", n)
	}
}

// TestARendererNobodyClaimedIsStillClosed covers the leak the concurrent connect makes
// possible: the wait for a playable buffer can end the cast while discovery is still running,
// so the session that arrives afterwards is one nothing will ever ask for. Leaving it open
// leaves a renderer holding a control session castor has forgotten about.
func TestARendererNobodyClaimedIsStillClosed(t *testing.T) {
	dev := &closingDevice{}
	h := newHeld(func(context.Context) (Renderer, error) { return dev, nil })

	if _, err := h.get(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.close()
	if !dev.closed.Load() {
		t.Error("the renderer was never closed")
	}

	// And closing one that was never acquired is not a nil dereference: it is the ordinary
	// teardown of a cast that failed before it composed.
	newHeld(func(context.Context) (Renderer, error) { return nil, errors.New("never connected") }).close()
}

// closingDevice is a renderer that only records being released.
type closingDevice struct {
	fakeDevice
	closed atomic.Bool
}

func (d *closingDevice) Close() error {
	d.closed.Store(true)
	return nil
}

// TestAReadOnceCastConnectsWhileItReads is the whole reason the connect timing is a column: a
// cast whose shape is already fixed must not sequence discovery ahead of the read, because the
// source URL is single-use and short-lived and SSDP discovery plus connect take seconds of its
// life.
//
// The origin here answers nothing until it is released, so the read cannot have got anywhere.
// A cast that connected only when it needed the renderer would not have called this connect
// yet.
func TestAReadOnceCastConnectsWhileItReads(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(origin.Close)
	sourceURL, err := url.Parse(origin.URL + "/movie.mp4")
	if err != nil {
		t.Fatal(err)
	}

	connecting := make(chan struct{})
	connect := func(context.Context, core.Config) (device.Device, error) {
		close(connecting)
		return &fakeDevice{caps: dlnaLike()}, nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- castOnce(ctx, t, castConfig(device.TypeDLNA, ffmpegPath, ffprobePath), connect, &media.Stream{URL: sourceURL, ContentType: media.MP4})
	}()

	select {
	case <-connecting:
	case err := <-done:
		t.Fatalf("the cast ended before the renderer was connected: %v", err)
	case <-time.After(castTimeout):
		t.Fatal("the renderer was not connected while the source was still answering nothing, so discovery is ageing the source URL ahead of the read")
	}

	close(release)
	cancel()
	<-done
}

// legFacts are the source facts a wiring function may not branch on. Every one of them is
// something a stream's pathology changes, and every one of them belongs to a rule: which
// composition runs, how the source is read, what the container can carry, whether the cast is
// still healthy. A wiring function that tested one would be a second, unnamed rule, which is
// exactly what the two long functions this table replaced had grown.
var legFacts = []string{
	"ContentType", "Headers", "AudioURL", "NeedsLeniency", "Live", "Bandwidth", "SelfFetchable", "Demuxed",
	"VideoCodec", "AudioCodec", "AudioChannels", "VideoHeight", "VideoProfile", "VideoHDR",
	"Framing", "Measured", "Probe", "Speed", "Position", "SelfFetch", "Containers", "AcceptsContainer",
}

// TestNoWiringFunctionBranchesOnASourceFact reads the wiring itself, because the property is
// about the shape of the code and not about an output: any of these branches would still
// produce a passing cast for the sources the suite happens to drive, and would quietly become
// the place the next pathology is handled.
//
// The composition rules and the decisions they name are of course free to read these facts.
// What is checked is the four functions that only wire: the three legs and the two functions
// that compose and run them.
func TestNoWiringFunctionBranchesOnASourceFact(t *testing.T) {
	wiring := []string{"readOnce", "startReading", "serveBuffer", "remux", "passthrough", "run", "compose", "serve"}

	fset := token.NewFileSet()
	for _, file := range []string{"leg_readonce.go", "leg_remux.go", "leg_passthrough.go", "session.go", "compose.go"} {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !slices.Contains(wiring, fn.Name.Name) || fn.Body == nil {
				continue
			}
			for _, fact := range conditionedOn(fn.Body) {
				if slices.Contains(legFacts, fact) {
					t.Errorf("%s branches on %s, which is a source fact and therefore a rule's answer to give",
						fn.Name.Name, fact)
				}
			}
		}
	}
}

// conditionedOn is every name a function's branches are decided by: the selectors and calls
// appearing in an if condition or a switch tag.
func conditionedOn(body *ast.BlockStmt) []string {
	var names []string
	collect := func(cond ast.Node) {
		if cond == nil {
			return
		}
		ast.Inspect(cond, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.SelectorExpr:
				names = append(names, e.Sel.Name)
			case *ast.Ident:
				names = append(names, e.Name)
			}
			return true
		})
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.IfStmt:
			collect(s.Cond)
		case *ast.SwitchStmt:
			collect(s.Tag)
		}
		return true
	})
	return names
}

// TestEveryAttemptOwnsAFreshWorkDirectoryAndLeavesNoneBehind is what makes a revised cast
// safe to offer at all.
//
// Nothing in castor seeks and no buffer is rewindable, so an attempt that reused the last
// one's directory would tail a file already holding the abandoned read's bytes: the encoder
// would serve the failed attempt's opening minutes and then whatever the new read appended
// to them. The other half is that the abandoned one is unlinked, since a cast that keeps
// every attempt's buffer fills the disk with titles nobody is watching.
func TestEveryAttemptOwnsAFreshWorkDirectoryAndLeavesNoneBehind(t *testing.T) {
	var dirs []string
	withCompositions(t, []composition{{
		name:  "records its work directory",
		why:   "the property under test is the directory, not the cast",
		needs: profileOnly,
		when:  func(core.Shape) bool { return true },
		run: func(_ context.Context, c *cast) landing {
			dirs = append(dirs, c.workDir)
			if _, err := os.Stat(c.workDir); err != nil {
				t.Errorf("the leg ran with no work directory to buffer into: %v", err)
			}
			return landing{err: errors.New("this attempt failed")}
		},
	}})

	target := &fakeTarget{profile: pushOnly(), caps: dlnaLike()}
	source := &media.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example"}, ContentType: media.MP4}
	for range 2 {
		run(t.Context(), core.Config{}, target, attempt.Attempt{Source: source}, "127.0.0.1")
	}

	if len(dirs) != 2 || dirs[0] == dirs[1] {
		t.Fatalf("two attempts ran in %v, want two different directories: a buffer nothing can rewind must not be reused", dirs)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("the abandoned attempt's directory %s is still on disk (%v)", dir, err)
		}
	}
}

// withCompositions swaps the shipped table for one row, so a property of the lifecycle
// around a leg is exercisable without a real cast, a real renderer or an ffmpeg.
func withCompositions(t *testing.T, rows []composition) {
	t.Helper()
	shipped := compositions
	compositions = rows
	t.Cleanup(func() { compositions = shipped })
}
