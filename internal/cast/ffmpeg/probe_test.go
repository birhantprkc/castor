package ffmpeg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// TestSourceProbeZeroesTheAudioHalfWhenTheRenditionFails covers a demuxed program
// whose audio rendition cannot be read. The encode maps 1:a:0 on this shape, so
// the video rendition's own audio track is not the track being encoded, and
// leaving it in the returned ProbeInfo means every downstream decision is about
// the wrong stream. That used to only mis-choose copy-vs-encode; now it also
// chooses a bitstream filter, and both wrong answers are severe (the wrong codec
// is exit 234 at filter init with the output never opened, the wrong direction is
// exit 0 with destroyed audio).
func TestSourceProbeZeroesTheAudioHalfWhenTheRenditionFails(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	// The video rendition deliberately CARRIES an audio track, so a stale answer
	// would look perfectly plausible and this test would pass by accident if the
	// zeroing were removed.
	fixture := generateFixture(t, ffmpegPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/video.mp4" {
			http.ServeFile(w, r, fixture)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	src := NetworkSource{
		URL:      mustURL(t, srv.URL+"/video.mp4"),
		AudioURL: mustURL(t, srv.URL+"/audio.m3u8"), // 404s
	}

	info, err := SourceProbe(ffprobePath, src).Probe(t.Context())
	if err == nil {
		t.Fatal("want an error for an unreadable audio rendition, got nil")
	}
	if info.VideoCodec != media.CodecH264 {
		t.Errorf("video codec = %q, want h264: the video half still decides the video axis", info.VideoCodec)
	}
	if info.AudioCodec != "" || info.AudioChannels != 0 {
		t.Errorf("audio half = %q/%d, want zero: it describes input 0 while the encode maps input 1",
			info.AudioCodec, info.AudioChannels)
	}
	// And a zeroed audio half must reach no adaptation, on any destination.
	for _, f := range []media.FormatInfo{mpegtsFormat, mp4Format, hlsFormat} {
		if plan := planAudioCopy(info, f); plan.Filters != nil {
			t.Errorf("an unprobed audio track produced %+v for %s", plan, f.ContentType)
		}
	}
}

// TestSourceProbeWaitsOutARateLimiterTheReaderWouldHaveWaitedOut is the whole point
// of the probe carrying the read policy. This probe exists to decide copy-vs-encode
// before a byte is pulled, and every caller treats its failure as "nothing is known
// against this source", so a probe that gives up where the reader would have
// persisted costs a source its stream copy for a reason the read never had.
//
// The origin answers the first request 429 and serves the file afterwards, which is
// exactly the shape the reader's reconnect terms exist for: with them ffprobe prints
// "HTTP error 429 Too Many Requests", then "Will reconnect at 0 in 1 second(s)", and
// answers; without them it fails at open with "Server returned 429 Too Many
// Requests".
func TestSourceProbeWaitsOutARateLimiterTheReaderWouldHaveWaitedOut(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	fixture := generateFixture(t, ffmpegPath)

	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		http.ServeFile(w, r, fixture)
	}))
	defer srv.Close()

	policy, err := read.For(read.Shape{}, testReadDeadline)
	if err != nil {
		t.Fatal(err)
	}
	src := NetworkSource{URL: mustURL(t, srv.URL+"/video.mp4"), ContentType: media.MP4, Read: policy}

	info, err := SourceProbe(ffprobePath, src).Probe(t.Context())
	if err != nil {
		t.Fatalf("the probe gave up on a 429 the reader would have waited out: %v", err)
	}
	if info.VideoCodec != media.CodecH264 {
		t.Errorf("video codec = %q, want h264", info.VideoCodec)
	}
	if got := requests.Load(); got < 2 {
		t.Errorf("the origin was asked %d time(s); the retry that proves the terms reached ffprobe never happened", got)
	}
}

// TestSourceProbeNamesItsOwnDeadline covers the failure that used to arrive with no
// evidence at all. A source that accepts the connection and then answers nothing is
// killed at its caller's deadline, and what came back was "ffprobe: signal: killed":
// castor's own impatience, reported as ffprobe misbehaving, with nothing to say which
// party stopped first. Every caller treats a failed probe as "nothing is known
// against this source" and reads on, so the one thing this error owes its reader is
// which of the two clocks ran out.
func TestSourceProbeNamesItsOwnDeadline(t *testing.T) {
	_, ffprobePath := requireFFmpegTools(t)

	// Accepts, then holds the request open until the probe is killed under it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	src := NetworkSource{URL: mustURL(t, srv.URL+"/silent.mp4"), ContentType: media.MP4, Read: longGETRead}

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	_, err := SourceProbe(ffprobePath, src).Probe(ctx)
	if err == nil {
		t.Fatal("want an error for a source that answered nothing, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, which does not carry the deadline that caused it", err)
	}
	if !strings.Contains(err.Error(), "deadline") {
		t.Errorf("error = %q, which does not say that castor stopped waiting rather than ffprobe failing", err)
	}
}

// generateFixture writes a one second H.264/AAC mp4 and returns its path. It carries
// both tracks so a probe of it can be wrong in either direction rather than only
// absent.
func generateFixture(t *testing.T, ffmpegPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.mp4")
	gen := exec.CommandContext(t.Context(), ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=5:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-shortest",
		"-movflags", "+faststart",
		path,
	)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating fixture: %v\n%s", err, out)
	}
	return path
}

// requireFFmpegTools returns the ffmpeg and ffprobe paths, or skips: this test
// generates a real fixture and probes it.
func requireFFmpegTools(t *testing.T) (ffmpegPath, ffprobePath string) {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH; skipping the probe test (it generates a real fixture)")
	}
	ffprobePath, err = exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH; skipping the probe test")
	}
	if _, err := os.Stat(ffprobePath); err != nil {
		t.Skip("ffprobe not usable; skipping the probe test")
	}
	return ffmpegPath, ffprobePath
}
