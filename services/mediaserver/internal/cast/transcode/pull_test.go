package transcode

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/fetch"
	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// A real pull against a real origin, reading the spool, progress and PCM outputs.
func TestTheExtraPipesCarryWhatTheFlagsSay(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	fixture := filepath.Join(t.TempDir(), "video.mp4")
	gen := exec.CommandContext(t.Context(), ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=5:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-shortest",
		"-movflags", "+faststart", fixture)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating fixture: %v\n%s", err, out)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, fixture)
	}))
	defer srv.Close()

	opts := copyingPull(muxedSource(t, mustURL(t, srv.URL+"/video.mp4"), media.MP4, fetch.For(media.Fetch{}, 30*time.Second)))
	opts.PCMSampleRate = 16000
	cmd, err := PullArgs(opts)
	if err != nil {
		t.Fatal(err)
	}

	var (
		samples []media.Progress
		pcm     countingWriter
	)
	proc, err := ffmpeg.Start(t.Context(), ffmpegPath, cmd, ffmpeg.Options{PCM: &pcm,
		Progress: func(s media.Progress) { samples = append(samples, s) }})
	if err != nil {
		t.Fatal(err)
	}
	spooled, _ := io.Copy(io.Discard, proc.Stdout)
	if err := proc.Wait(); err != nil {
		t.Fatalf("the pull failed: %v\n%q", err, proc.Evidence().Lines)
	}

	if spooled == 0 {
		t.Error("the spool output produced nothing")
	}
	// One second of mono s16le at 16 kHz is 32000 bytes.
	if pcm.n < 16000 {
		t.Errorf("PCM feed delivered %d bytes", pcm.n)
	}
	if len(samples) == 0 {
		t.Fatal("no progress samples arrived")
	}
	last := samples[len(samples)-1]
	if last.Position <= 0 || last.Speed <= 0 || last.Bytes <= 0 {
		t.Errorf("final sample = %+v, want a position, a size and a speed", last)
	}
	if got := proc.Progress(); got != last {
		t.Errorf("Progress() = %+v, want the last sample %+v", got, last)
	}
}

// countingWriter is safe to read after Wait, which joins the tee.
type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}
