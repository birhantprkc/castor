package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/media"
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
	tail, markers := newTail(), &markerWatch{}
	drainStderr(t.Context(), strings.NewReader(strings.Join(lines, "\n")+"\n"), tail, markers)
	return tail, markers
}

func TestWaitDoesNotOutrunTheStderrDrain(t *testing.T) {
	const last = "castor-final-stderr-line"
	script := "sleep 0.2; i=0; while [ $i -lt 5000 ]; do echo \"line $i\" >&2; i=$((i+1)); done; echo " + last + " >&2"

	proc, err := Start(t.Context(), "/bin/sh", Command{Args: []string{"-c", script}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
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
		{
			// An exited, unreaped process still accepts a kill, so a crash by SIGKILL (the OOM killer's) must not read as castor's.
			name:   "a SIGKILL of its own that castor's Kill arrives after keeps its signal",
			script: "kill -KILL $$",
			stop: func(p *Process, _ context.CancelFunc) {
				<-p.scanned
				p.Kill()
			},
			want: signalExitBase + int(syscall.SIGKILL),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			proc, err := Start(ctx, "/bin/sh", Command{Args: []string{"-c", tc.script}}, Options{})
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

// An ffmpeg that dies before opening its side outputs must not leave their readers waiting for a connection.
func TestSideOutputsEndWhenFFmpegNeverOpensThem(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	cmd := NewCommand([]string{"-hide_banner", "-i", "/nonexistent/castor-input",
		"-progress", ProgressPipe, "-f", "s16le", PCMPipe})
	var pcm bytes.Buffer
	proc, err := Start(t.Context(), ffmpegPath, cmd, Options{PCM: &pcm, Progress: func(media.Progress) {}})
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() {
		_, _ = io.Copy(io.Discard, proc.Stdout)
		waited <- proc.Wait()
	}()
	select {
	case err := <-waited:
		if err == nil {
			t.Fatal("ffmpeg succeeded on a missing input")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait never returned: a side output's reader is still waiting for a connection that will not come")
	}
	if pcm.Len() != 0 {
		t.Errorf("got %d PCM bytes from an output ffmpeg never opened", pcm.Len())
	}
}
