package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/resolve"
)

// fakeDevice is a Device stand-in that records what Run tells it to Play. On a
// served cast it also fetches the URL it is handed: the replay-served path blocks
// until a client reads the stream to EOF, so without a real reader Wait would hang
// the whole idle-grace window. Pass-through leaves drain false: the source URL is
// not one of our servers and must never be fetched.
//
// caps is the renderer's whole side of the contract, ServedContainer included:
// every served cast, the read-once spool now among them, takes the container it
// produces from the device's own declaration, so a fake that declares none is a
// renderer castor has nothing to serve.
type fakeDevice struct {
	caps  media.Renderer
	drain bool
	// refuse, when set, is what this renderer answers Play with, so a cast can be driven
	// against a renderer that will not play what it is pointed at.
	refuse error
	// tee, when set, receives the served bytes as they are drained, so a test can
	// inspect what the renderer was actually handed.
	tee io.Writer
	// detach makes Play return as soon as the renderer has accepted the URL and fetch the
	// stream on a goroutine of its own, which is what a real renderer does. A Play that only
	// returns once the whole title has arrived cannot be used to test anything that happens
	// WHILE a renderer is playing: the cast would already be over.
	detach bool
	// played, when set, is signalled on every Play. It is what lets a test of a
	// cast that never ends wait for playback to start instead of polling for it.
	played chan playCall

	mu    sync.Mutex
	plays []playCall
}

type playCall struct {
	url         string
	contentType string
}

// Compile-time proof the fake satisfies the interface Run consumes.
var _ device.Device = (*fakeDevice)(nil)

func (d *fakeDevice) Play(ctx context.Context, streamURL *url.URL, contentType string) error {
	play := playCall{url: streamURL.String(), contentType: contentType}
	d.mu.Lock()
	d.plays = append(d.plays, play)
	d.mu.Unlock()

	if d.played != nil {
		select {
		case d.played <- play:
		default: // a test that stops listening must not wedge the cast
		}
	}
	if d.refuse != nil {
		return d.refuse
	}
	if !d.drain {
		return nil
	}
	if d.detach {
		go func() { _ = d.fetch(ctx, streamURL) }()
		return nil
	}
	return d.fetch(ctx, streamURL)
}

// fetch drains the served stream to EOF so the replay server's Wait can complete.
// Bounded by ctx (the test sets a timeout), so a wedged producer fails the test
// rather than hanging it.
func (d *fakeDevice) fetch(ctx context.Context, streamURL *url.URL) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var sink io.Writer = io.Discard
	if d.tee != nil {
		sink = d.tee
	}
	_, err = io.Copy(sink, resp.Body)
	return err
}

func (d *fakeDevice) Capabilities() media.Renderer           { return d.caps }
func (d *fakeDevice) StreamHeaders(string) map[string]string { return nil }
func (d *fakeDevice) Close() error                           { return nil }

func (d *fakeDevice) snapshot() []playCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.plays)
}

// connectTo is the injected ConnectFunc that hands Run a pre-built fake instead of
// discovering a real renderer, so the executor runs without a live network.
func connectTo(dev device.Device) ConnectFunc {
	return func(context.Context, core.Config) (device.Device, error) { return dev, nil }
}

// A cast is one row of the matrix: a source shape, the renderer it is cast to,
// and what that renderer must end up holding. Every served-path case is stated
// this way, because they differ only in those three things. Written as separate
// functions they hid that, and a case could quietly drift into asserting
// something its neighbours did not.
type castCase struct {
	name string

	// The source. headers marks one that only ever answered to what castor
	// captured, which is what forces a relay even from a renderer that would
	// otherwise have been handed the URL.
	source  func(*testing.T, string) fixtureOrigin
	headers http.Header

	// declared is the height the SOURCE published for the rung this cast reads (an HLS
	// RESOLUTION, carried on the attempt as media.Rendition.Height). It is the only thing a
	// cast knows about its own height before it reads a byte, so it is what the composition
	// asks the ceiling about, and 0 (the ordinary case) means the source declared nothing.
	declared int

	// ceiling raises the cast's configured max_height for a row about what the ceiling
	// ADMITS rather than what it refuses, 0 to keep the suite's 1080.
	ceiling int

	// The renderer: a family, which fixes when castor connects, and the
	// capabilities it negotiates.
	family   device.Type
	caps     media.Renderer
	delivery core.DeliveryPreference

	// What must come out. served is the content type the device is told it is
	// fetching, empty for a cast castor should hand over untouched. The rest are
	// checked against a probe of the bytes the renderer actually received and are
	// skipped when zero, so a row asserts what it is about and nothing more.
	served   string
	video    media.Codec
	audio    media.Codec
	channels int
	// notVideo names a codec the renderer must NOT have been handed, for a row
	// whose point is that something was re-encoded rather than copied.
	notVideo media.Codec
	// height is the picture height the renderer must have received, for a row about
	// the cast's ceiling. It is exact rather than a bound: what castor produces is
	// scaled to the ceiling, so a row that asserted "no taller than" would pass on a
	// leg that dropped the source's resolution to a thumbnail.
	height int
}

// chromecastLike and dlnaLike are the two renderer shapes the rows are built
// from: one that fetches for itself and is served a fragmented mp4, one that
// never fetches and is served MPEG-TS. A row overrides only what it is about.
func chromecastLike(accepts ...string) media.Renderer {
	return media.Renderer{
		SelfFetch:       true,
		Containers:      accepts,
		ServedContainer: media.MP4,
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
	}
}

func dlnaLike() media.Renderer {
	return media.Renderer{
		Video:           []media.VideoSupport{{Codec: media.CodecH264}},
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
		ServedContainer: media.MPEGTS,
	}
}

// TestCastMatrix drives Run end to end for every source shape castor has to
// handle, against both renderer families.
//
// Every assertion is on the bytes the renderer received, never on what castor
// decided along the way: a cast is correct when a player could decode what it was
// given, and each decision this exercises is only a means to that.
func TestCastMatrix(t *testing.T) {
	cases := []castCase{{
		// The renderer fetches for itself and already accepts the container, so
		// castor has nothing to add and must stay out of the way.
		name:   "a source the renderer can fetch is handed over untouched",
		source: serveFixture,
		family: device.TypeChromecast,
		caps:   chromecastLike(media.MP4),
	}, {
		// Same renderer, same container, but the source only ever answered to the
		// headers castor captured. A URL carries none of them, so handing it over
		// leaves the device idling on a refusal.
		name:    "a header-gated source is relayed even to a renderer that accepts it",
		source:  serveFixture,
		headers: http.Header{"Referer": {"https://player.example/"}, "Origin": {"https://player.example"}},
		family:  device.TypeChromecast,
		caps:    chromecastLike(media.MP4),
		served:  media.MP4,
	}, {
		// The embed-CDN shape: MPEG-TS segments served as image/jpeg. Castor reads
		// it only by relaxing its reader's extension checks, so a renderer applying
		// its own would refuse the segments. Its AAC is ADTS-framed, which the mp4
		// muxer rejects outright unless the copy is repacked.
		name:    "a source only a lenient reader opens is relayed, and its ADTS audio survives",
		source:  serveHLSFixture,
		headers: http.Header{"Referer": {"https://player.example/"}},
		family:  device.TypeChromecast,
		caps:    chromecastLike(media.HLS, media.MP4),
		served:  media.MP4,
		video:   media.CodecH264,
		audio:   media.CodecAAC,
	}, {
		name:     "the operator's serve preference relays a source nothing else would",
		source:   serveFixture,
		family:   device.TypeChromecast,
		caps:     chromecastLike(media.MP4),
		delivery: core.DeliveryServe,
		served:   media.MP4,
	}, {
		name:   "a renderer that rejects the container gets a network remux",
		source: serveFixture,
		family: device.TypeChromecast,
		caps:   chromecastLike(media.MKV),
		served: media.MP4,
	}, {
		name:   "a renderer that never fetches gets the read-once spool",
		source: serveFixture,
		family: device.TypeDLNA,
		caps:   dlnaLike(),
		served: media.MPEGTS,
	}, {
		// Two renditions read as one program. Reading only the video half is the
		// failure this catches, and it is silent: the cast plays in silence.
		name:   "a demuxed program keeps its audio through the spool",
		source: serveDemuxedHLSFixture,
		family: device.TypeDLNA,
		caps:   dlnaLike(),
		served: media.MPEGTS,
		video:  media.CodecH264,
		audio:  media.CodecAAC,
	}, {
		// The same program into a container that declares its decoder configuration
		// up front. The repack the muxer demands is an output option, so it has to
		// follow the mapped track to the SECOND input; a rule that looked at input 0
		// would decide about the wrong stream entirely.
		name:   "a demuxed program keeps its audio through a remux",
		source: serveDemuxedHLSFixture,
		family: device.TypeChromecast,
		caps:   chromecastLike(media.MKV),
		served: media.MP4,
		video:  media.CodecH264,
		audio:  media.CodecAAC,
	}, {
		// AC-3 derives its dac3 sample description from the first frame, so a
		// fragmented mp4 that has already written its moov refuses to write a header
		// at all: exit 234, "Cannot write moov atom before AC3 packets", 941 bytes of
		// ftyp and nothing else.
		name:   "a copied Dolby track survives a fragmented mp4",
		source: serveAC3Fixture,
		family: device.TypeChromecast,
		caps: media.Renderer{
			SelfFetch: true, Containers: []string{media.HLS}, ServedContainer: media.MP4,
			Audio: []media.AudioSupport{{Codec: media.CodecAC3}},
		},
		served: media.MP4,
		audio:  media.CodecAC3,
	}, {
		// The same header rule applies to a Dolby track castor PRODUCED. This row
		// exists because the fix for the copied case did not cover the encoded one,
		// and every multichannel source a Cast receiver cannot decode goes through
		// here.
		name:   "an encoded Dolby track survives a fragmented mp4",
		source: serveSurroundFLACFixture,
		family: device.TypeChromecast,
		caps: media.Renderer{
			SelfFetch: true, Containers: []string{media.HLS}, ServedContainer: media.MP4,
			Audio: []media.AudioSupport{
				{Codec: media.CodecAAC, MaxChannels: 6}, {Codec: media.CodecAC3}, {Codec: media.CodecEAC3},
			},
		},
		served:   media.MP4,
		audio:    media.CodecEAC3,
		channels: 6,
	}, {
		// A codec the mp4 family has no tag for. It has to be re-encoded rather than
		// copied, or the muxer refuses to write a header and the cast produces
		// nothing at all.
		name:     "a codec the container cannot carry is re-encoded",
		source:   serveTheoraFixture,
		family:   device.TypeChromecast,
		caps:     chromecastLike(media.HLS),
		served:   media.MP4,
		notVideo: media.Codec("theora"),
	}, {
		// MPEG-TS has no stream type for FLAC and does not say so: it writes the
		// track as private data and exits cleanly, so the spool would arrive silent.
		name:   "audio the spool container cannot carry is re-encoded into it",
		source: serveFLACFixture,
		family: device.TypeDLNA,
		caps:   dlnaLike(),
		served: media.MPEGTS,
		audio:  media.CodecAAC,
	}, {
		// The video half of the same trap, and the more dangerous one: VP9 into
		// -f mpegts exits 0 having reported hundreds of KB muxed, and the output has
		// no video stream at all. The spool pull is a bare copy, so such a source is
		// destroyed before any encoder sees it.
		name:     "video the spool container cannot carry is re-encoded into it",
		source:   serveVP9Fixture,
		family:   device.TypeDLNA,
		caps:     dlnaLike(),
		served:   media.MPEGTS,
		video:    media.CodecH264,
		notVideo: media.CodecVP9,
	}, {
		// The cast's height ceiling on the leg that used to drop it. Which composition a
		// cast lands on is settled by what a renderer answered during discovery, and nothing
		// tells a user which one happened, so a ceiling honoured on the buffered leg and
		// short-circuited on this one meant the same max_height delivered 1080p or the
		// source's full 1440p depending on a routing decision reported nowhere.
		//
		// Everything else about this source is copyable: h264 this renderer decodes, into a
		// container that carries it, so the ceiling is the only thing left that can refuse
		// it. Only the source container is wrong, which is what makes this a remux.
		name:   "a source over the cast's ceiling is scaled down on the remux too",
		source: serveTallFixture,
		family: device.TypeChromecast,
		caps: media.Renderer{
			SelfFetch: true, Containers: []string{media.MP4}, ServedContainer: media.MP4,
			Video: []media.VideoSupport{{Codec: media.CodecH264}},
			Audio: []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
		},
		served: media.MP4,
		height: 1080,
	}, {
		// The ceiling on the leg that cannot scale anything, which is the leg where it costs
		// the most and is the user's instruction all the same: max_height is a maximum on what
		// reaches the RENDERER, not on what castor's encoder produces. This renderer fetches
		// for itself and accepts the source container, so the source would have been handed
		// over as it stands and delivered 1440 lines to an operator who asked for 1080, with
		// nothing downstream to say so (the legs that consult the ceiling are the legs that
		// measure something, and this one reads no bytes at all).
		//
		// So the shape is refused instead and the cast falls to the remux, which reads the
		// source and scales it. That is the expensive leg and it wants a hardware encoder to
		// hold realtime for a whole title: the trade is deliberate, because casting more than
		// was asked for is not something castor may do quietly.
		name:     "a source declared above the cast's ceiling is served scaled rather than handed over",
		source:   serveTallFixture,
		declared: 1440,
		family:   device.TypeChromecast,
		caps:     chromecastLike(media.MKV),
		served:   media.MP4,
		height:   1080,
	}, {
		// The carve-out, end to end, and it is required rather than a softening of the row
		// above. This is the identical cast whose source declared no height, which is what
		// nearly every pass-through looks like (a direct file, a media playlist with no
		// RESOLUTION), and a pass-through measures nothing ever, so absence of evidence is all
		// castor will ever have. Convicting on it would cost castor its cheapest leg almost
		// entirely, to bound a picture that in all likelihood already fits.
		name:   "a source that declared no height is handed over untouched under the same ceiling",
		source: serveTallFixture,
		family: device.TypeChromecast,
		caps:   chromecastLike(media.MKV),
	}, {
		// The same declared 1440 lines under an operator who asked for 2160: nothing is over
		// the ceiling, so the cheapest leg is the correct one and the renderer is handed the
		// URL. This is the row that pins the ceiling as the CONFIGURED number rather than
		// whatever a composition happened to be built with, because a ceiling wired in as zero
		// refuses every declared height and would look exactly like a working one from the row
		// above.
		name:     "a source declared under a raised ceiling is handed over after all",
		source:   serveTallFixture,
		declared: 1440,
		ceiling:  2160,
		family:   device.TypeChromecast,
		caps:     chromecastLike(media.MKV),
	}, {
		// A pinned stream map turns a missing track into an argument-parse failure
		// before a single byte is read. The optional suffix is what keeps such a
		// source castable rather than refused, on both served shapes.
		name:   "a source with no audio track is remuxed rather than refused",
		source: serveVideoOnlyFixture,
		family: device.TypeChromecast,
		caps:   chromecastLike(media.MKV),
		served: media.MP4,
		video:  media.CodecH264,
	}, {
		name:   "a source with no audio track is spooled rather than refused",
		source: serveVideoOnlyFixture,
		family: device.TypeDLNA,
		caps:   media.Renderer{Video: []media.VideoSupport{{Codec: media.CodecH264}}, ServedContainer: media.MPEGTS},
		served: media.MPEGTS,
		video:  media.CodecH264,
	}}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ffmpegPath, ffprobePath := requireFFmpegTools(t)

			origin := tt.source(t, ffmpegPath)
			source := origin.stream()
			source.Headers = tt.headers

			// Capture what the renderer receives, so the assertions read the bytes a
			// player would have had to decode rather than castor's account of them.
			received := filepath.Join(t.TempDir(), "received")
			sink, err := os.Create(received)
			if err != nil {
				t.Fatal(err)
			}
			defer sink.Close()

			dev := &fakeDevice{caps: tt.caps, drain: tt.served != "", tee: sink}
			cfg := castConfig(tt.family, ffmpegPath, ffprobePath)
			cfg.Delivery = tt.delivery
			if tt.ceiling > 0 {
				cfg.Resolver.MaxHeight = tt.ceiling
			}

			ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
			defer cancel()
			if err := castRung(ctx, t, cfg, connectTo(dev), source, media.Rendition{Height: tt.declared}); err != nil {
				t.Fatalf("cast: %v", err)
			}

			plays := dev.snapshot()
			if len(plays) != 1 {
				t.Fatalf("expected exactly one Play call, got %d: %+v", len(plays), plays)
			}
			if tt.served == "" {
				if plays[0].url != source.URL.String() {
					t.Errorf("Play url = %q, want the source URL %q: this renderer fetches for itself",
						plays[0].url, source.URL)
				}
				if plays[0].contentType != source.ContentType {
					t.Errorf("Play content type = %q, want the source's own %q", plays[0].contentType, source.ContentType)
				}
				return
			}
			if plays[0].contentType != tt.served {
				t.Errorf("served content type = %q, want %q", plays[0].contentType, tt.served)
			}
			if plays[0].url == source.URL.String() {
				t.Errorf("a relayed cast must not point the device at the source URL %q", source.URL)
			}

			if err := sink.Close(); err != nil {
				t.Fatal(err)
			}
			assertReceived(t, ffprobePath, received, tt)
		})
	}
}

// assertReceived probes what the renderer was handed. A declared track is not
// evidence on its own: a fragmented mp4 declares everything up front, so a stream
// that carries no packets at all probes exactly like a healthy one.
func assertReceived(t *testing.T, ffprobePath, path string, tt castCase) {
	t.Helper()

	if tt.video == "" && tt.notVideo == "" && tt.audio == "" && tt.channels == 0 && tt.height == 0 {
		return
	}
	info, err := ffmpeg.FileProbe(ffprobePath, path).Probe(t.Context())
	if err != nil {
		t.Fatalf("probing what the renderer received: %v", err)
	}
	if tt.video != "" && info.VideoCodec != tt.video {
		t.Errorf("received video codec = %q, want %q", info.VideoCodec, tt.video)
	}
	if tt.notVideo != "" && info.VideoCodec == tt.notVideo {
		t.Errorf("received video codec = %q, which this container cannot carry", info.VideoCodec)
	}
	if tt.audio != "" && info.AudioCodec != tt.audio {
		t.Errorf("received audio codec = %q, want %q", info.AudioCodec, tt.audio)
	}
	if tt.channels > 0 && info.AudioChannels != tt.channels {
		t.Errorf("received %d audio channels, want %d: a codec the renderer decodes must not be downmixed",
			info.AudioChannels, tt.channels)
	}
	if tt.height > 0 && info.VideoHeight != tt.height {
		t.Errorf("received a %dp picture, want %dp: the cast's height ceiling bounds what castor produces on every leg",
			info.VideoHeight, tt.height)
	}
	if tt.video != "" || tt.notVideo != "" || tt.height > 0 {
		if n := videoPackets(t, ffprobePath, path); n == 0 {
			t.Error("the received stream declares video but carries no packets")
		}
	}
	if tt.audio != "" || tt.channels > 0 {
		if n := audioPackets(t, ffprobePath, path); n == 0 {
			t.Error("the received stream declares audio but carries no packets")
		}
	}
}

// TestCastLiveHLS is the one served shape the matrix cannot express, because it
// never ends on its own: a renderer served a playlist (a Roku) is cast a window
// that rolls for as long as the source does, so the cast is ended by cancelling
// rather than by running out of input, and what it produced has to be read while
// it is still running.
//
// The source is deliberately the HLS fixture and not the mp4 one. The mp4
// fixture's AAC is already out of band, so it passes with the repack reverted and
// proves nothing; the HLS fixture's segments carry ADTS, which the hls muxer's
// fMP4 segments refuse outright ("Malformed AAC bitstream detected", exit 255, no
// init.mp4, no segments, no playlist at all).
func TestCastLiveHLS(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	origin := serveHLSFixture(t, ffmpegPath)
	source := origin.stream()

	dev := &fakeDevice{
		caps: media.Renderer{
			SelfFetch:       true,
			Containers:      []string{media.MKV}, // rejects the HLS source, so it is remuxed
			ServedContainer: media.HLS,
			Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
		},
		played: make(chan playCall, 4),
	}

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- castOnce(ctx, t, castConfig(device.TypeChromecast, ffmpegPath, ffprobePath), connectTo(dev), source)
	}()

	// Wait to be told rather than polling: the fake signals on Play, so this
	// proceeds the instant playback starts and cannot pass or fail on how long the
	// remux happened to take.
	var play playCall
	select {
	case play = <-dev.played:
	case err := <-done:
		t.Fatalf("Run returned before playback started: %v", err)
	case <-ctx.Done():
		t.Fatal("device was never pointed at the HLS playlist")
	}

	if play.contentType != media.HLS {
		t.Errorf("served content type = %q, want %q", play.contentType, media.HLS)
	}
	if !strings.HasSuffix(play.url, ".m3u8") {
		t.Errorf("an HLS cast must point the device at a .m3u8 playlist, got %q", play.url)
	}
	if play.url == source.URL.String() {
		t.Error("a relayed cast must not hand the device the source URL")
	}

	// Count what reached the segments, over the served playlist while the cast is
	// still live. A declared track is no more evidence here than on the mp4 path:
	// what this guards is that the ADTS-framed source audio was repacked into the
	// fMP4 segments' framing rather than dropped on the floor.
	awaitAudioPackets(ctx, t, ffprobePath, play.url)

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}

	// Still exactly one Play after the encoder stopped. A second would mean castor
	// changed its mind mid-cast and pointed the renderer somewhere else, which no
	// receiver recovers from.
	if plays := dev.snapshot(); len(plays) != 1 {
		t.Errorf("expected exactly one Play, got %d: %+v", len(plays), plays)
	}
}

// TestEncoderFailureFailsTheCast is the direct regression test for "castor exited
// 0 having cast nothing". A dead encoder used to be indistinguishable from a
// finished one: io.Copy sees a clean EOF, the replay server's Wait returns nil
// once no client is left, and the exit status was logged at WARN after Serve had
// already returned success.
func TestEncoderFailureFailsTheCast(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	// An origin that answers every request with 404, so ffmpeg cannot open its
	// input and exits non-zero having written nothing.
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(srv.Close)
	sourceURL, err := url.Parse(srv.URL + "/gone.mp4")
	if err != nil {
		t.Fatal(err)
	}
	source := &media.Stream{URL: sourceURL, ContentType: media.MP4}

	dev := &fakeDevice{
		caps: media.Renderer{
			SelfFetch:       true,
			Containers:      []string{media.MKV}, // rejects the source container -> remux
			ServedContainer: media.MP4,
		},
		drain: true,
	}

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()

	if err := castOnce(ctx, t, castConfig(device.TypeChromecast, ffmpegPath, ffprobePath), connectTo(dev), source); err == nil {
		t.Fatal("the cast reported success though its encoder died before producing anything")
	}
}

// TestADeadReadIsReportedAsTheReadsOwnFailure is the mislabelling this executor's outcome
// exists to end, end to end over a real reader.
//
// The source answers 404 to everything, so the puller's ffmpeg exits non-zero having landed
// nothing. That error used to travel out through the spool's write side and the encoder's
// stdin, collecting a prefix at each, and reach a user as "encoder: spool producer failed:
// upstream pull: exit status 183": the encoder was the only party named and the only one
// that had done nothing wrong.
func TestADeadReadIsReportedAsTheReadsOwnFailure(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(srv.Close)
	sourceURL, err := url.Parse(srv.URL + "/gone.mp4")
	if err != nil {
		t.Fatal(err)
	}
	source := &media.Stream{URL: sourceURL, ContentType: media.MP4}

	// A renderer that never fetches for itself, so this is the read-once leg: the one with a
	// reader of castor's own behind an encoder that can be blamed for it.
	dev := &fakeDevice{caps: dlnaLike()}

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()

	policy, err := read.For(read.ShapeOf(media.Origin{}), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cfg := castConfig(device.TypeDLNA, ffmpegPath, ffprobePath)
	out := NewExecutor(cfg, connectTo(dev), "127.0.0.1").Run(ctx, attempt.Attempt{
		Try: 1, Source: source, Read: policy,
	})

	if out.Err == nil {
		t.Fatal("the cast reported success though its source answered 404 to everything")
	}
	if out.Evidence.ReadErr == nil {
		t.Error("the outcome carries no terminal error for the read that died, so nothing above it can blame the read")
	}
	if !errors.Is(out.Err, out.Evidence.ReadErr) {
		t.Errorf("the cast reports %q, which does not carry the read's own error %q", out.Err, out.Evidence.ReadErr)
	}
	if strings.Contains(out.Err.Error(), "encoder:") {
		t.Errorf("the cast reports %q, blaming the encoder for a source that answered 404", out.Err)
	}
	if out.Reached() != attempt.PhaseReading {
		t.Errorf("reached %s, want %s: no renderer was ever pointed at anything", out.Reached(), attempt.PhaseReading)
	}
	if len(out.Evidence.Lines) == 0 {
		t.Error("the outcome retains none of what the reader printed, which is all a user has to act on")
	}
	// The two facts a classification of the READ is built from, over a real reader. Neither is
	// inferable from the error: the exit status is a number a rule may read where the message
	// carrying it is prose it may not, and nothing about a source's codecs says whether this
	// reader was passing its packets through or producing them.
	if out.Evidence.ReadExit <= 0 {
		t.Errorf("the outcome reports exit status %d for a reader that exited on its own account, so a broken copy cannot be told from a read castor killed",
			out.Evidence.ReadExit)
	}
	if got := out.Evidence.Copied; !got.Video || !got.Audio {
		t.Errorf("the outcome says the reader was copying %s, want both halves: nothing was measured against this source, so both were passed through", got)
	}
}

// TestAReadThatDiesWithARendererPlayingIsNeverCastAgain is decision 1 over the real machinery,
// and it is the case that was missing: every other test of the rule hand-builds an evidence
// value claiming PhasePlaying, a phase no leg here ever produced.
//
// It is the failure at the worst moment it can happen. The read has landed seconds of media,
// the buffer is playable, the renderer holds the URL and is fetching it, and then the source
// hands over a fragment whose bitstream cannot be resynchronised and the reader exits 183.
// Reported as reading, that is a broken copy with a recovery waiting for it, and the recovery is
// a fresh work directory, a fresh connect and a fresh Play: the film started over from the
// beginning for whoever was watching it.
func TestAReadThatDiesWithARendererPlayingIsNeverCastAgain(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	origin, breakTheFragments := serveBreakableFMP4Fixture(t, ffmpegPath)
	source := origin.stream()

	// A renderer that never fetches for itself, so this is the read-once leg: the one with a
	// reader of castor's own behind an encoder that gets blamed for it. It fetches on a
	// goroutine, because a Play that only returned at the end of the title would put the
	// reader's death before playback rather than during it.
	dev := &fakeDevice{caps: dlnaLike(), drain: true, detach: true, played: make(chan playCall, 1)}

	// Two links, so the refusal is the rule's doing and not an ordering with nowhere left to
	// go: the cast this test asserts about has a recovery available for its class and must
	// still decline it.
	second := *source
	secondURL := *source.URL
	secondURL.Path = "/second.m3u8"
	second.URL = &secondURL

	// fMP4 fragments, which is the read policy such a source gets in production: the one shape
	// where a read abandoned partway through a fragment is worse than waiting for it.
	shape := media.Origin{Segmented: true, Framing: media.FramingOutOfBand}
	policy, err := read.For(read.ShapeOf(shape), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	in := attempt.Intent{
		Candidates: []*media.Stream{source, &second},
		Origin:     shape,
		Read:       policy,
		Deadline:   30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()

	runner := &tally{run: NewExecutor(castConfig(device.TypeDLNA, ffmpegPath, ffprobePath), connectTo(dev), "127.0.0.1")}
	done := make(chan error, 1)
	go func() { done <- attempt.Cast(ctx, in, runner, asPublished{}) }()

	// Wait to be told rather than polling: the renderer signals on Play, so the link expires at
	// the one instant this test is about and cannot expire early on a slow machine.
	select {
	case <-dev.played:
	case err := <-done:
		t.Fatalf("the cast ended before any renderer was playing: %v", err)
	case <-ctx.Done():
		t.Fatal("no renderer was ever pointed at the buffer")
	}
	breakTheFragments()

	if err := <-done; err == nil {
		t.Fatal("the cast reported success though its source read died mid-title")
	}
	if len(runner.outcomes) != 1 {
		t.Fatalf("ran %d attempts, want 1: a cast someone is watching is never started over", len(runner.outcomes))
	}
	if plays := dev.snapshot(); len(plays) != 1 {
		t.Fatalf("the renderer was played %d times, want 1: no receiver recovers from being pointed somewhere else mid-title", len(plays))
	}

	out := runner.outcomes[0]
	if out.Reached() != attempt.PhasePlaying {
		t.Errorf("reached %s, want %s: the renderer had accepted the URL and was fetching when the read died",
			out.Reached(), attempt.PhasePlaying)
	}
	// The two facts that make this the reviewed failure rather than a quiet one, and the two
	// that classified it as a broken copy: a POSITIVE exit status (a reader castor killed has
	// none) over axes the reader was passing through untouched.
	if out.Evidence.ReadExit <= 0 {
		t.Errorf("the outcome reports exit status %d, want the reader's own: this case is about a read that failed at something it was doing",
			out.Evidence.ReadExit)
	}
	if got := out.Evidence.Copied; !got.Video || !got.Audio {
		t.Errorf("the outcome says the reader was copying %s, want both halves", got)
	}
	// And the recovery that was available and declined, so this cannot pass on a playbook with
	// nothing to offer.
	if _, ok := attempt.DecodeAxis.Apply(ctx, attempt.Change{Outcome: out}); !ok {
		t.Error("the recovery for this class declined on its own account, so the refusal was not the phase's doing")
	}
}

// tally is the executor with a record of every outcome it answered, which is the only way to
// assert how many attempts a cast ran: the renderer's Play calls cannot say it (an attempt that
// fails earlier makes none), and the error a cast ends with is the last attempt's alone.
//
// The record is written by the goroutine driving the cast and read after it has finished, so
// the channel that reports the cast's result orders the two.
type tally struct {
	run      attempt.Runner
	outcomes []attempt.Outcome
}

func (c *tally) Run(ctx context.Context, a attempt.Attempt) attempt.Outcome {
	out := c.run.Run(ctx, a)
	c.outcomes = append(c.outcomes, out)
	return out
}

// asPublished is the source layer as a recovery reaches it, for links whose documents say
// nothing new: the link is handed back untouched, which is the answer resolution itself gives
// for a playlist it could not fetch.
type asPublished struct{}

func (asPublished) Refetch(_ context.Context, s *media.Stream) (*media.Stream, media.Origin, media.Rendition, error) {
	return s, media.Origin{}, media.Rendition{}, nil
}

// serveBreakableFMP4Fixture publishes a generated program as fMP4 HLS fragments, one fragment
// per request and never faster than fragmentDelay, and once the returned function is called it
// hands over fragments whose media payload cannot be read.
//
// That is the failure castor's own read table is written around, at the one moment it cannot be
// recovered from. A fragment abandoned mid-read is truncated, a truncated AVCC stream
// desynchronises the h264_mp4toannexb filter that a copy into MPEG-TS cannot do without, and the
// reader exits 183 on "Invalid NAL unit size" (the observed line read "-1140850681 > 97253").
// The bytes are 0xff rather than absent because a length prefix the filter cannot believe is
// what makes it fatal: a merely SHORT fragment does not, and neither does a refused one. ffmpeg
// reads a short fragment and stops at exit 0, and it skips a fragment it cannot open at all
// (it advances the sequence number and carries on), so both end a read that failed at nothing
// and neither reaches the class this test is about.
//
// The pacing is the other half of making this testable. An unthrottled origin hands over the
// whole program in a couple of hundred milliseconds, which is quicker than the playback gate's
// own confidence window, so the read would be finished before any renderer had been pointed at
// anything and there would be nothing left alive to break. Paced, it delivers two and a half
// media seconds per wall-clock second: well clear of playback, so the deliverability rule has
// nothing to say about it, and slow enough that the reader is still running for seconds after
// the renderer starts playing.
func serveBreakableFMP4Fixture(t *testing.T, ffmpegPath string) (fixtureOrigin, func()) {
	t.Helper()
	dir := t.TempDir()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=" + strconv.Itoa(breakableFixture),
		"-f", "lavfi", "-i", "sine=frequency=440:duration=" + strconv.Itoa(breakableFixture),
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline", "-g", "15",
		"-c:a", "aac", "-ac", "2", "-shortest",
		"-f", "hls",
		"-hls_time", "1",
		"-hls_list_size", "0",
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(dir, "seg_%03d.m4s"),
		filepath.Join(dir, "index.m3u8"),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("generating the fMP4 fixture: %v\n%s", err, out)
	}
	// The next link the ranker admitted, published over the same fragments: what a recovery
	// would move onto, so a cast declining to move is visible rather than merely out of moves.
	playlist, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "second.m3u8"), playlist, 0o600); err != nil {
		t.Fatal(err)
	}

	var broken atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := os.ReadFile(filepath.Join(dir, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !strings.HasSuffix(r.URL.Path, ".m3u8") {
			time.Sleep(fragmentDelay)
		}
		if broken.Load() && strings.HasSuffix(r.URL.Path, ".m4s") {
			// The mdat payload and nothing else: the fragment's boxes stay well formed, so what
			// the reader chokes on is the bitstream it was copying and not a container it could
			// have refused up front.
			if i := bytes.Index(body, []byte("mdat")); i >= 0 {
				payload := body[i+4:]
				for k := range payload {
					payload[k] = 0xff
				}
			}
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	return fixtureOrigin{server: server, path: "/index.m3u8", contentType: media.HLS},
		func() { broken.Store(true) }
}

// The breakable fixture's shape: how much program it publishes, and how long the origin holds
// each one-second fragment back. Their product is the wall-clock life the reader has, which has
// to outlast the playback gate's confidence window on any host, and their ratio is the delivery
// speed, which has to stay clear of playback so that nothing else convicts this read first.
const (
	breakableFixture = 24
	fragmentDelay    = 400 * time.Millisecond
)

// castOnce drives one attempt through the executor and reports what it ended with,
// which is the whole of what these tests exercise: every case here states a single
// source, so the attempt loop above would run exactly this one attempt and hand back
// exactly this error.
//
// The attempt states a zero media.Origin, and truthfully so: these tests drive the
// executor directly, so no source resolution ran and there is no document whose facts
// could have been harvested. The read policy is the one such a source gets.
func castOnce(ctx context.Context, t *testing.T, cfg core.Config, connect ConnectFunc, source *media.Stream) error {
	t.Helper()
	// A zero rung is the honest value for the same reason the zero Origin is: nothing resolved
	// a document here, so nothing declared a height.
	return castRung(ctx, t, cfg, connect, source, media.Rendition{})
}

// castRung is castOnce for a cast whose source DECLARED which rung it is serving. That fact
// travels on the attempt and nowhere else, and it is the whole input to the ceiling's half of
// the composition question: what the source said, before castor has read a byte of it.
func castRung(ctx context.Context, t *testing.T, cfg core.Config, connect ConnectFunc, source *media.Stream, rung media.Rendition) error {
	t.Helper()
	policy, err := read.For(read.ShapeOf(media.Origin{}), cfg.Transcode.RWTimeout)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	// The operator's preference reaches the executor on the ATTEMPT and nowhere else,
	// which is where a recovery would change it: an executor reading its own configuration
	// instead would be answering a question that has since been asked again.
	delivery := cfg.Delivery
	cfg.Delivery = core.DeliveryAuto
	return NewExecutor(cfg, connect, "127.0.0.1").Run(ctx, attempt.Attempt{
		Try:       1,
		Source:    source,
		Rendition: rung,
		Read:      policy,
		Delivery:  delivery,
	}).Err
}

// castTimeout bounds every cast this suite drives. It is a backstop, not a
// schedule: nothing here waits it out, so a wedged ffmpeg fails one test rather
// than hanging the binary until the go test deadline kills it with a stack dump.
const castTimeout = 90 * time.Second

// castConfig is the configuration a served-path test runs on: the device family
// (which fixes the connect timing) plus the real ffmpeg tools. A test that needs
// to vary an axis takes this and edits the field it cares about.
func castConfig(deviceType device.Type, ffmpegPath, ffprobePath string) core.Config {
	return core.Config{
		Device:    core.DeviceConfig{Type: deviceType},
		Transcode: core.TranscodeConfig{FFmpegPath: ffmpegPath, RWTimeout: 30 * time.Second},
		// ProbeTimeout is required rather than optional, here as in production: it
		// bounds the source probe, and a zero value is an expired deadline rather than
		// an absent one, so every probe fails instantly and each axis it measures
		// silently falls back. Leaving it unset is what turned the two carriage cells
		// below into a bare copy into a container that cannot carry it.
		Resolver: resolve.Config{FFprobePath: ffprobePath, MaxHeight: 1080, ProbeTimeout: 30 * time.Second},
	}
}

// fixtureOrigin is a local HTTP origin serving generated media, standing in for
// the upstream a real cast pulls from: path is what the cast points at and
// contentType the container it resolved to.
type fixtureOrigin struct {
	server      *httptest.Server
	path        string
	audioPath   string // set when the origin publishes audio as its own rendition
	contentType string
}

// stream is the media.Stream a cast resolves to for this origin. audioPath is
// set only for a demuxed origin, where resolution yields two URLs.
func (o fixtureOrigin) stream() *media.Stream {
	u, _ := url.Parse(o.server.URL + o.path)
	s := &media.Stream{URL: u, ContentType: o.contentType}
	if o.audioPath != "" {
		audio, _ := url.Parse(o.server.URL + o.audioPath)
		s.AudioURL = audio
	}
	return s
}

// serveFixture generates a one-second H.264/AAC mp4 and serves it over local
// HTTP. faststart puts the moov atom up front so ffmpeg can read it streaming,
// and http.ServeFile still honours range requests the demuxer may issue.
func serveFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "aac", "-ac", "2", "-shortest",
		"-movflags", "+faststart",
		path,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("generating fixture: %v\n%s", err, out)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, path)
	}))
	t.Cleanup(server.Close)
	return fixtureOrigin{server: server, path: "/movie.mp4", contentType: media.MP4}
}

// serveHLSFixture generates a short HLS stream whose MPEG-TS segments are written
// under a .jpg extension and serves the directory over local HTTP, which labels
// them image/jpeg from that extension. That is the disguise embed CDNs use:
// ffmpeg reads it (castor relaxes the extension checks for every HLS input),
// while a Cast receiver handed the same playlist rejects the segments.
func serveHLSFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	dir := t.TempDir()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "aac", "-ac", "2", "-shortest",
		"-f", "hls",
		"-hls_time", "1",
		"-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(dir, "seg_%03d.jpg"),
		filepath.Join(dir, "index.m3u8"),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("generating HLS fixture: %v\n%s", err, out)
	}

	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)
	return fixtureOrigin{server: server, path: "/index.m3u8", contentType: media.HLS}
}

// serveDemuxedHLSFixture generates an HLS stream whose video and audio are
// published as separate renditions, the shape a master with an EXT-X-MEDIA audio
// group resolves to: one playlist of video segments, one of audio segments, and
// neither is playable on its own.
func serveDemuxedHLSFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	dir := t.TempDir()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "aac", "-ac", "2", "-shortest",
		"-f", "hls",
		"-hls_time", "1",
		"-hls_list_size", "0",
		"-var_stream_map", "v:0,agroup:aud a:0,agroup:aud",
		filepath.Join(dir, "rendition_%v.m3u8"),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("generating demuxed HLS fixture: %v\n%s", err, out)
	}

	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)
	return fixtureOrigin{
		server:      server,
		path:        "/rendition_0.m3u8",
		audioPath:   "/rendition_1.m3u8",
		contentType: media.HLS,
	}
}

// Fixture lengths. shortFixture is what a test uses when the source is only a
// carrier for the codec or container it is named after.
//
// spooledFixture is for the read-once tests: a program long enough that its
// MPEG-TS spool passes the pull's carriage checkpoint well before the download
// ends, delivered over spooledFixtureDelivery so that the download is still
// running while the stages downstream of it make their decisions (see
// serveGeneratedOver).
const (
	shortFixture           = 1
	spooledFixture         = 20
	spooledFixtureDelivery = 2 * time.Second
)

// serveAC3Fixture generates an MKV carrying AC-3, the codec whose sample
// description box is derived from its first frame and which therefore needs the
// mp4 muxer's moov held back.
func serveAC3Fixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGenerated(t, ffmpegPath, "fixture.mkv", "/movie.mkv", media.MKV, shortFixture,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "ac3", "-ac", "2", "-shortest",
	)
}

// serveSurroundFLACFixture is a 5.1 source in a codec no renderer castor targets
// advertises, so core.DecideAudio cannot copy it and climbs to its Dolby rung.
func serveSurroundFLACFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGenerated(t, ffmpegPath, "surround.mkv", "/surround.mkv", media.MKV, shortFixture,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "flac", "-ac", "6", "-shortest",
	)
}

// serveTheoraFixture is a container castor accepts carrying a codec no table of
// castor's mentions, which is the only way to test that the tables are not the
// authority.
//
// It is also the suite's only source that reaches a remux re-encode, so it is
// generated above the cast's height ceiling: a leg that re-encodes and forgets to
// bound what it produces is otherwise indistinguishable from one that does not,
// and the ceiling is exactly what the remux used to drop on the way to its ladder.
func serveTheoraFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGenerated(t, ffmpegPath, "fixture.mkv", "/movie.mkv", media.MKV, shortFixture,
		"-vf", "scale=1920:1440",
		"-c:v", "libtheora", "-c:a", "aac", "-ac", "2", "-shortest",
	)
}

// serveTallFixture is a program above the cast's height ceiling that is copyable on every
// other ground: h264 inside a container castor's destinations carry, with stereo AAC. The
// ceiling is the only thing that can refuse it, which is what makes what the renderer
// receives an answer about the ceiling and nothing else.
//
// MKV because that is what leaves the ceiling as the only reason a self-fetching renderer
// could be refused this URL: a renderer that rejects MKV is served a remux whatever the
// heights say, so the rows that are about the ceiling are the ones where the renderer accepts
// it and the composition still turns on what the source declared.
func serveTallFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGenerated(t, ffmpegPath, "tall.mkv", "/tall.mkv", media.MKV, shortFixture,
		"-vf", "scale=1920:1440",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "aac", "-ac", "2", "-shortest",
	)
}

// serveFLACFixture generates an MKV carrying FLAC: a codec the MPEG-TS spool has
// no stream type for, which it writes as private data at exit 0. It is served
// paced because the pull's recovery has to hold on a source whose download is
// still running when the stages downstream of the pull start reading its spool.
func serveFLACFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGeneratedOver(t, ffmpegPath, "fixture.mkv", "/movie.mkv", media.MKV, spooledFixture, spooledFixtureDelivery,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-b:v", "400k", "-profile:v", "baseline",
		"-c:a", "flac", "-ac", "2", "-shortest",
	)
}

// serveVP9Fixture generates a WebM carrying VP9 video and Opus audio. MPEG-TS
// carries Opus with proper stream registration and destroys VP9, so only the
// video axis of the pull has to degrade. Served the same way, for the same
// reason, as the FLAC fixture above.
func serveVP9Fixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGeneratedOver(t, ffmpegPath, "fixture.webm", "/movie.webm", media.WebM, spooledFixture, spooledFixtureDelivery,
		"-c:v", "libvpx-vp9", "-b:v", "600k", "-deadline", "realtime", "-cpu-used", "8",
		"-pix_fmt", "yuv420p",
		"-c:a", "libopus", "-ac", "2", "-shortest",
	)
}

// serveVideoOnlyFixture generates an mp4 with no audio track at all, the shape a
// pinned -map turns into an argument-parse failure before a byte is read.
func serveVideoOnlyFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGenerated(t, ffmpegPath, "fixture.mp4", "/movie.mp4", media.MP4, shortFixture,
		"-map", "0:v", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-movflags", "+faststart",
	)
}

// serveGenerated builds a test program of the given length with the given output
// options and serves it over local HTTP at wire speed. Every fixture reads the
// same synthetic video and tone, so a test's name describes only the axis it
// varies.
func serveGenerated(t *testing.T, ffmpegPath, filename, urlPath, contentType string, seconds int, outputArgs ...string) fixtureOrigin {
	t.Helper()
	path := generateFixture(t, ffmpegPath, filename, seconds, outputArgs)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, path)
	}))
	t.Cleanup(server.Close)
	return fixtureOrigin{server: server, path: urlPath, contentType: contentType}
}

// serveGeneratedOver is serveGenerated over an origin that takes roughly the
// given wall-clock time to hand over the whole file, which is the only way a
// local test can model the one property of a real source that the read-once path
// is built around: the download outlives the decisions taken about it.
//
// An unthrottled localhost origin delivers a twenty second fixture in about two
// hundred milliseconds, faster than one tick of the playback gate, so the pull
// reaches its terminal state before anything downstream has looked at the spool.
// Under that timing every ordering rule between the pull's carriage verdict and
// the readers that tail its artifact holds by accident, including the one that
// makes the restart legal (Spool.Reset refuses once a tail exists). Pacing the
// origin is what makes those rules load-bearing in the test the way they are in a
// cast.
func serveGeneratedOver(t *testing.T, ffmpegPath, filename, urlPath, contentType string, seconds int, over time.Duration, outputArgs ...string) fixtureOrigin {
	t.Helper()
	path := generateFixture(t, ffmpegPath, filename, seconds, outputArgs)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	perByte := over / time.Duration(max(info.Size(), 1))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		// ServeContent rather than a hand-rolled body: the demuxers reading these
		// fixtures issue range requests (an MKV seeks to its cues), and a paced
		// origin that answered those with the whole file would fail the read for a
		// reason that has nothing to do with pacing.
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), &pacedFile{f: f, perByte: perByte})
	}))
	t.Cleanup(server.Close)
	return fixtureOrigin{server: server, path: urlPath, contentType: contentType}
}

// pacedFile is a seekable file that hands back its bytes at a fixed rate.
type pacedFile struct {
	f       *os.File
	perByte time.Duration
}

func (p *pacedFile) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	if n > 0 {
		time.Sleep(p.perByte * time.Duration(n))
	}
	return n, err
}

func (p *pacedFile) Seek(offset int64, whence int) (int64, error) {
	return p.f.Seek(offset, whence)
}

// generateFixture renders one synthetic program to disk and returns its path.
func generateFixture(t *testing.T, ffmpegPath, filename string, seconds int, outputArgs []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	duration := strconv.Itoa(seconds)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=" + duration,
		"-f", "lavfi", "-i", "sine=frequency=440:duration=" + duration,
	}
	args = append(args, outputArgs...)
	args = append(args, path)

	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("generating %s: %v\n%s", filename, err, out)
	}
	return path
}

// awaitAudioPackets waits for a live playlist to carry sound. Polling is right
// here and nowhere else in this suite: the window genuinely grows over time, and
// there is no event to wait on. It is bounded by the cast's own context, so it
// cannot outlive what it is measuring.
func awaitAudioPackets(ctx context.Context, t *testing.T, ffprobePath, playlistURL string) {
	t.Helper()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	for {
		if n := countPackets(t, ffprobePath, "a:0", playlistURL); n > 0 {
			return
		}
		select {
		case <-poll.C:
		case <-ctx.Done():
			t.Fatal("the served HLS window carries no audio packets: the ADTS source was not repacked into the fMP4 segments' framing")
		}
	}
}

// audioPackets counts the audio packets actually present in path, which is the
// only way to tell a served stream that carries sound from one that merely
// declares a track it never managed to mux.
func audioPackets(t *testing.T, ffprobePath, path string) int {
	t.Helper()
	return countPackets(t, ffprobePath, "a:0", path)
}

// videoPackets is the video twin, for the shapes where the track that vanished is
// the picture rather than the sound (VP9 into MPEG-TS reads back as bin_data with
// no video stream at all, at exit 0 with hundreds of KB written).
func videoPackets(t *testing.T, ffprobePath, path string) int {
	t.Helper()
	return countPackets(t, ffprobePath, "v:0", path)
}

// countPackets counts the packets ffprobe can actually read off one stream. It
// never fails the test: a source ffprobe cannot open, or a track it cannot count,
// is exactly the "declares a track and carries nothing" answer the callers are
// looking for, and it is also what a live playlist looks like before its first
// segment has been cut.
func countPackets(t *testing.T, ffprobePath, stream, path string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	var args []string
	if strings.HasSuffix(path, ".m3u8") {
		// A served playlist is read with the same relaxations castor reads every
		// HLS input with, so this measures what the renderer would get.
		args = append(args, media.HLSInputArgs...)
	}
	args = append(args,
		"-v", "error",
		"-select_streams", stream,
		"-count_packets",
		"-show_entries", "stream=nb_read_packets",
		"-of", "csv=p=0",
		path,
	)
	out, err := exec.CommandContext(ctx, ffprobePath, args...).Output()
	if err != nil {
		return 0
	}
	// ffprobe's csv writer emits one row per matching stream and pads rows with a
	// trailing separator, and an MPEG-TS input repeats the stream once per program,
	// so take the first numeric field rather than the whole blob. "N/A" is the
	// answer a track that declares itself and carries nothing gives, which is the
	// result these callers are looking for and not a parse failure.
	for line := range strings.SplitSeq(string(out), "\n") {
		field, _, _ := strings.Cut(strings.TrimSpace(line), ",")
		if field == "" || field == "N/A" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("packet count %q for %s in %s: %v", field, stream, path, err)
		}
		return n
	}
	return 0
}

// requireFFmpegTools returns the ffmpeg and ffprobe paths, or skips: the served
// branches drive a real remux and probe, so a host without them cannot run them.
func requireFFmpegTools(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH; skipping the served-path engine test (it drives a real remux)")
	}
	ffprobe, err = exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH; skipping the served-path engine test (it probes the stream)")
	}
	return ffmpeg, ffprobe
}
