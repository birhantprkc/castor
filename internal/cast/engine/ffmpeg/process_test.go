package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/media"
)

func TestSilentFailuresAreCaughtOnAnyLine(t *testing.T) {
	for _, marker := range []string{
		"[mpegts @ 0x1] AAC bitstream not in ADTS format and extradata missing",
		"[mp4 @ 0x2] Malformed AAC bitstream detected: use the audio bitstream filter 'aac_adtstoasc' to fix it",
	} {
		t.Run(marker, func(t *testing.T) {
			// The marker scrolls out of the bounded tail and must still be remembered.
			lines := []string{marker}
			for i := range stderrTailCapacity * 2 {
				lines = append(lines, fmt.Sprintf("frame= %d fps=25", i))
			}
			tail, markers := drainLines(t, lines...)
			if markers.failure() == nil {
				t.Error("the marker was forgotten once it left the tail")
			}
			if got := tail.snapshot(); len(got) != stderrTailCapacity || slices.Contains(got, marker) {
				t.Errorf("the tail holds %d lines, want the last %d", len(got), stderrTailCapacity)
			}
		})
	}

	t.Run("a clean transcript reports nothing", func(t *testing.T) {
		if _, markers := drainLines(t, "Input #0, mpegts, from 'pipe:0':", "frame=  250 fps=0.0"); markers.failure() != nil {
			t.Errorf("clean run reported %v", markers.failure())
		}
	})
}

// A blocking write into a full stderr pipe stops the encode dead, so an over-long line must not stop the drain.
func TestTheDrainOutlastsALineTooLongToHold(t *testing.T) {
	marker := "[mp4 @ 0x1] Malformed AAC bitstream detected"
	tail, markers := drainLines(t, strings.Repeat("x", stderrLineBuffer*3), marker)
	if markers.failure() == nil || !slices.Contains(tail.snapshot(), marker) {
		t.Error("nothing printed after the over-long line was read")
	}
}

func drainLines(t *testing.T, lines ...string) (*ringTail, *markerWatch) {
	t.Helper()
	tail, markers := newTail(stderrTailCapacity), &markerWatch{}
	var to fanout
	to.add(tail)
	to.add(markers)
	drainStderr(t.Context(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &to)
	return tail, markers
}

func TestWaitDoesNotOutrunTheStderrDrain(t *testing.T) {
	const last = "castor-final-stderr-line"
	script := "sleep 0.2; i=0; while [ $i -lt 200 ]; do echo \"line $i\" >&2; i=$((i+1)); done; echo " + last + " >&2"

	proc, err := Start(t.Context(), "/bin/sh", Command{Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	proc.lines.add(slowObserver{})
	if _, err := io.Copy(io.Discard, proc.Stdout); err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	if lines := proc.Evidence().Lines; !slices.Contains(lines, last) {
		t.Errorf("the last line printed before exit is not in the %d retained", len(lines))
	}
}

type slowObserver struct{}

func (slowObserver) Observe(string) { time.Sleep(time.Millisecond) }

func TestAnExitStatusSaysWhoEndedTheProcess(t *testing.T) {
	cases := []struct {
		name   string
		script string
		stop   func(*Process, context.CancelFunc)
		want   int
	}{
		{name: "an exit keeps its code", script: "exit 3", want: 3},
		{name: "a signal of its own is an exit that failed", script: "kill -SEGV $$", want: signalExitBase + int(syscall.SIGSEGV)},
		{name: "castor's Kill leaves no status", script: "sleep 30", stop: func(p *Process, _ context.CancelFunc) { p.Kill() }, want: noExitStatus},
		{name: "castor's cancellation leaves no status", script: "sleep 30", stop: func(_ *Process, cancel context.CancelFunc) { cancel() }, want: noExitStatus},
		{
			// Teardown always kills before it waits, so a crash must survive the kill that follows it.
			name:   "a crash that castor's Kill arrives after keeps its own signal",
			script: "kill -ABRT $$",
			stop: func(p *Process, _ context.CancelFunc) {
				<-p.scanned
				p.Kill()
			},
			want: signalExitBase + int(syscall.SIGABRT),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			proc, err := Start(ctx, "/bin/sh", Command{Args: []string{"-c", tc.script}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.stop != nil {
				tc.stop(proc, cancel)
			}
			_ = proc.Wait()
			if got := proc.Evidence().ExitStatus; got != tc.want {
				t.Errorf("ExitStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

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

	opts := copyingPull(muxedSource(t, mustURL(t, srv.URL+"/video.mp4"), media.MP4, read.For(media.Fetch{}, 30*time.Second)))
	opts.PCM, opts.PCMSampleRate = true, 16000
	cmd, err := PullArgs(opts)
	if err != nil {
		t.Fatal(err)
	}

	var (
		samples []media.Progress
		pcm     countingWriter
	)
	proc, err := Start(t.Context(), ffmpegPath, cmd, WithPCM(&pcm),
		WithProgress(func(s media.Progress) { samples = append(samples, s) }))
	if err != nil {
		t.Fatal(err)
	}
	spooled, _ := io.Copy(io.Discard, proc.Stdout)
	if err := proc.Wait(); err != nil {
		t.Fatalf("the pull failed: %v\n%q", err, proc.StderrTail())
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
