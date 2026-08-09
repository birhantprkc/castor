package ffprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// TestOpensUnaided covers the measurement that tells a source castor can hand
// over from one only castor can read. Both playlists carry the same MPEG-TS
// segments; only the extension they are published under differs, which is the
// disguise embed CDNs use (a receiver sees image/jpeg and refuses the stream).
func TestOpensUnaided(t *testing.T) {
	prober := New(requireFFprobe(t), 30*time.Second)
	origin := serveHLSFixtures(t)

	for _, tc := range []struct {
		name     string
		playlist string
		want     bool
	}{
		{"conformant segments open under default checks", "/plain.m3u8", true},
		{"segments disguised as images do not", "/disguised.m3u8", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := prober.OpensUnaided(t.Context(), streamAt(t, origin+tc.playlist)); got != tc.want {
				t.Errorf("OpensUnaided = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMeasureReportsTheProgram pins what the ranker reads off a measurement. The
// disguised playlist is measured too, because that is the asymmetry the whole
// design rests on: castor's own reader relaxes the extension checks
// (media.HLSInputArgs), so a source it can cast measures cleanly here even though
// no renderer applying its own defaults would take it.
func TestMeasureReportsTheProgram(t *testing.T) {
	prober := New(requireFFprobe(t), 30*time.Second)
	origin := serveHLSFixtures(t)

	for _, playlist := range []string{"/plain.m3u8", "/disguised.m3u8"} {
		t.Run(playlist, func(t *testing.T) {
			info, reach, err := prober.Measure(t.Context(), streamAt(t, origin+playlist))
			if err != nil {
				t.Fatalf("Measure: %v", err)
			}
			if reach != media.ReachOpened {
				t.Errorf("Reach = %s, want opened: the origin served the source and ffprobe read it", reach)
			}
			if info.ContentType != media.HLS {
				t.Errorf("ContentType = %q, want %q", info.ContentType, media.HLS)
			}
			if !info.Playable() {
				t.Errorf("Playable = false (video %q, audio %q); the fixture carries both", info.VideoCodec, info.AudioCodec)
			}
			if info.VideoHeight != 240 {
				t.Errorf("VideoHeight = %d, want the fixture's 240", info.VideoHeight)
			}
			// The fixture is two seconds long, so it is VOD with a known duration. A
			// zero here would read as "live" and pace the whole cast at realtime.
			if info.Duration <= 0 {
				t.Errorf("Duration = %s, want the fixture's known runtime", info.Duration)
			}
		})
	}
}

// TestMeasureCarriesTheOriginsRefusalIntoTheError guards the one thing a caller
// can act on when a measurement fails: ffprobe writes why to stderr, and dropping
// it leaves the ranker logging a bare exit status for a link the origin explicitly
// refused. The Reach is the same fact promoted to something a rule can read, and it
// is asserted here against a real refusal because this is the only place the wording
// ffprobe actually emits is available: everything above this package is scripted.
func TestMeasureCarriesTheOriginsRefusalIntoTheError(t *testing.T) {
	prober := New(requireFFprobe(t), 30*time.Second)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	t.Cleanup(origin.Close)

	_, reach, err := prober.Measure(t.Context(), streamAt(t, origin.URL+"/signed.m3u8"))
	if err == nil {
		t.Fatal("a 403 must fail the measurement")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want the origin's status in it", err)
	}
	if reach != media.ReachRefused {
		t.Errorf("Reach = %s, want refused: a 403 is an answer, and it is the answer every reader gets", reach)
	}
}

// TestMeasureReportsAnUnprovenOriginAsUnproven is the other half, and it is the one
// with a cast riding on it: castor's own probe fan-out is what earns a 429, and a
// source that merely ran out of budget is still worth attempting. ffprobe is given a
// timeout it cannot meet against an origin that answers headers and then dribbles
// nothing, which is the tarpit shape a deadline exists for.
func TestMeasureReportsAnUnprovenOriginAsUnproven(t *testing.T) {
	prober := New(requireFFprobe(t), 300*time.Millisecond)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegURL")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(origin.Close)

	_, reach, err := prober.Measure(t.Context(), streamAt(t, origin.URL+"/slow.m3u8"))
	if err == nil {
		t.Fatal("a measurement that outran its budget must fail")
	}
	if reach != media.ReachUnproven {
		t.Errorf("Reach = %s, want unproven: castor killed its own probe, the origin refused nothing", reach)
	}
}

// TestClassifyReach pins the classification against the text ffmpeg's HTTP protocol
// actually writes, and pins the direction it is allowed to fail in. Every status
// that is not a final no stays unproven, including the ones it is tempting to
// convict on: a 429 is usually castor's own probe burst coming back, and a 503 is an
// origin having a bad minute. Neither says the puller cannot read the link.
func TestClassifyReach(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		want   media.Reach
	}{
		{"[http @ 0x14] HTTP error 403 Forbidden\nsigned.m3u8: Server returned 403 Forbidden (access denied)\n", media.ReachRefused},
		{"signed.m3u8: Server returned 404 Not Found\n", media.ReachRefused},
		{"signed.m3u8: Server returned 401 Unauthorized\n", media.ReachRefused},
		{"signed.m3u8: Server returned 410 Gone\n", media.ReachRefused},
		{"[http @ 0x14] HTTP error 429 Too Many Requests\n", media.ReachUnproven},
		{"signed.m3u8: Server returned 503 Service Unavailable\n", media.ReachUnproven},
		{"signed.m3u8: Connection reset by peer\n", media.ReachUnproven},
		{"movie.mp4: Invalid data found when processing input\n", media.ReachUnproven},
		{"", media.ReachUnproven},
	} {
		t.Run(strings.TrimSpace(tc.stderr), func(t *testing.T) {
			if got := classifyReach(tc.stderr); got != tc.want {
				t.Errorf("classifyReach = %s, want %s", got, tc.want)
			}
		})
	}
}

// serveHLSFixtures generates one conformant HLS stream and one whose MPEG-TS
// segments are published under a .jpg extension, and serves both from a single
// origin. It returns the origin's base URL.
func serveHLSFixtures(t *testing.T) string {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH; skipping the playlist-conformance fixtures")
	}
	dir := t.TempDir()

	for _, f := range []struct{ playlist, segments string }{
		{"plain.m3u8", "plain_%03d.ts"},
		{"disguised.m3u8", "disguised_%03d.jpg"},
	} {
		args := []string{
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=2",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
			"-c:a", "aac", "-ac", "2", "-shortest",
			"-f", "hls", "-hls_time", "1", "-hls_list_size", "0",
			"-hls_segment_filename", filepath.Join(dir, f.segments),
			filepath.Join(dir, f.playlist),
		}
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("generating %s: %v\n%s", f.playlist, err, out)
		}
	}

	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)
	return server.URL
}

// requireFFprobe returns the ffprobe path, or skips: every measurement here is
// ffprobe's own verdict, so there is nothing to test without it.
func requireFFprobe(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH; skipping the playlist-conformance tests")
	}
	return path
}

// streamAt is the subject a measurement takes: a bare URL with no headers, which
// is what the ranker hands it for a candidate nothing was captured for.
func streamAt(t *testing.T, raw string) *media.Stream {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &media.Stream{URL: u}
}
