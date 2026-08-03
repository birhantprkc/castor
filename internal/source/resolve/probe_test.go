package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

// TestOpensWithoutLeniency covers the measurement that tells a source castor can
// hand over from one only castor can read. Both playlists carry the same MPEG-TS
// segments; only the extension they are published under differs, which is the
// disguise embed CDNs use (a receiver sees image/jpeg and refuses the stream).
func TestOpensWithoutLeniency(t *testing.T) {
	ffprobePath := requireFFprobe(t)

	for _, tc := range []struct {
		name     string
		playlist string
		want     bool
	}{
		{"conformant segments open under default checks", "/plain.m3u8", true},
		{"segments disguised as images do not", "/disguised.m3u8", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := serveHLSFixtures(t)
			u, err := url.Parse(origin + tc.playlist)
			if err != nil {
				t.Fatal(err)
			}
			if got := opensWithoutLeniency(context.Background(), ffprobePath, 30*time.Second, u, nil); got != tc.want {
				t.Errorf("opensWithoutLeniency = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveMarksLenientOnlySource is the same fact end to end: a disguised
// source comes out of resolution not self-fetchable, so the planner serves it
// instead of handing a renderer a URL it will refuse. The conformant one keeps
// its pass-through.
func TestResolveMarksLenientOnlySource(t *testing.T) {
	ffprobePath := requireFFprobe(t)
	origin := serveHLSFixtures(t)

	for _, tc := range []struct {
		name            string
		playlist        string
		wantFetchable   bool
		wantNeedsLenity bool
	}{
		{"conformant source stays self-fetchable", "/plain.m3u8", true, false},
		{"disguised source is not self-fetchable", "/disguised.m3u8", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(origin + tc.playlist)
			if err != nil {
				t.Fatal(err)
			}
			stream := &media.Stream{URL: u, ContentType: media.HLS}
			cfg := Config{FFprobePath: ffprobePath, ProbeTimeout: 30 * time.Second, HLSTimeout: 30 * time.Second, MaxHeight: 1080}

			resolved, err := Resolve(context.Background(), cfg, stream)
			if err != nil {
				t.Fatal(err)
			}
			if got := resolved.NeedsLeniency; got != tc.wantNeedsLenity {
				t.Errorf("NeedsLeniency = %v, want %v", got, tc.wantNeedsLenity)
			}
			if got := resolved.SelfFetchable(); got != tc.wantFetchable {
				t.Errorf("SelfFetchable = %v, want %v", got, tc.wantFetchable)
			}
		})
	}
}

// TestResolveSkipsTheProbeItCannotUse guards the cost: a source already ruled out
// of pass-through is served whatever the measurement would say, so it must not be
// paid for. The ffprobe path is deliberately bogus, which would mark any source
// that actually ran it.
func TestResolveSkipsTheProbeItCannotUse(t *testing.T) {
	origin := serveHLSFixtures(t)
	u, err := url.Parse(origin + "/disguised.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	stream := &media.Stream{
		URL:         u,
		ContentType: media.HLS,
		Headers:     http.Header{"Referer": {"https://player.example/"}},
	}
	cfg := Config{FFprobePath: "/nonexistent-ffprobe", ProbeTimeout: 30 * time.Second, HLSTimeout: 30 * time.Second, MaxHeight: 1080}

	resolved, err := Resolve(context.Background(), cfg, stream)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.NeedsLeniency {
		t.Error("a header-gated source is served regardless; the conformance probe must not run")
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
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

// requireFFprobe returns the ffprobe path, or skips: the conformance measurement
// is ffprobe's own verdict, so there is nothing to test without it.
func requireFFprobe(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH; skipping the playlist-conformance tests")
	}
	return path
}
