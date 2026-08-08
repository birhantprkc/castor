package ffmpeg

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// TestSilentFailureDetector drives the stderr drain with real ffmpeg transcripts.
// Exit status is not a health signal for a stream copy: three known shapes
// exit 0 with destroyed media, and every one of them prints one of these lines,
// so the scan is the only evidence castor ever gets that an apparently clean run
// produced nothing playable.
func TestSilentFailureDetector(t *testing.T) {
	cases := []struct {
		name string
		line string
		// want names a fragment of the reason the detector must attach, so the
		// message stays actionable rather than merely non-nil.
		want string
	}{
		{
			// exit 0, the muxer rejects every packet and repeats itself 188 times.
			name: "a repack aimed at an in-band container",
			line: "[mpegts @ 0x14b704080] AAC bitstream not in ADTS format and extradata missing",
			want: "in band",
		},
		{
			// This one exits 255, but catching it on the first line names the fix.
			name: "ADTS AAC reaching an out-of-band container with no repack",
			line: "[mp4 @ 0x13d00a4c0] Malformed AAC bitstream detected: use the audio bitstream filter 'aac_adtstoasc' to fix it",
			want: "repack",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, markers := drainLines(t, healthyTranscript(c.line)...)
			err := markers.failure()
			if err == nil {
				t.Fatalf("no silent failure detected for %q", c.line)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("reason %q does not explain the failure (want a mention of %q)", err, c.want)
			}
			if !strings.Contains(err.Error(), c.line) {
				t.Errorf("reason %q does not quote the offending line", err)
			}
		})
	}

	t.Run("a clean transcript reports nothing", func(t *testing.T) {
		_, markers := drainLines(t, healthyTranscript()...)
		if err := markers.failure(); err != nil {
			t.Errorf("clean run reported %v", err)
		}
	})

	t.Run("the first hit is kept", func(t *testing.T) {
		// ffmpeg repeats these; the first is the one whose cause is still on screen.
		_, markers := drainLines(t,
			"[mpegts @ 0x1] AAC bitstream not in ADTS format and extradata missing",
			"[mp4 @ 0x2] Malformed AAC bitstream detected",
		)
		if err := markers.failure(); !strings.Contains(err.Error(), "in band") {
			t.Errorf("kept %v, want the first hit", err)
		}
	})

	t.Run("a recoverable refusal is not terminal", func(t *testing.T) {
		// A container with no stream type for a codec writes it as private data and
		// exits 0. That is a real failure, but it is one castor recovers from by
		// re-encoding, so it must not be reported as a dead cast here. The verdict on
		// it is read from the artifact instead (see the carriage package); treating
		// the line as terminal would kill the cast before the recovery could run.
		_, markers := drainLines(t, healthyTranscript(
			"[mpegts @ 0x1] Stream 1, codec flac, is muxed as a private data stream and may not be recognized upon reading.",
		)...)
		if err := markers.failure(); err != nil {
			t.Errorf("a recoverable carriage refusal reported as terminal: %v", err)
		}
	})
}

// drainLines pushes lines through the same fan-out the stderr reader uses, with the
// two observers Start registers, so both are exercised where they actually live
// rather than through a shortcut.
func drainLines(t *testing.T, lines ...string) (*ringTail, *markerWatch) {
	t.Helper()
	tail, markers := newTail(stderrTailCapacity), &markerWatch{}
	var to fanout
	to.add(tail)
	to.add(markers)
	drainStderr(t.Context(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &to)
	return tail, markers
}

// TestEveryObserverSeesEveryLine is the fan-out's whole claim. Retention and marker
// detection used to be one type holding one lock, so anything else that wanted to read
// what ffmpeg said had to be that type: the AVCC desync's cause was reachable only by
// whoever also owned the ring buffer, and it sat about a hundred lines into a window
// that keeps a hundred and twenty eight.
func TestEveryObserverSeesEveryLine(t *testing.T) {
	transcript := healthyTranscript("[mpegts @ 0x1] AAC bitstream not in ADTS format and extradata missing")

	var first, second collector
	var to fanout
	to.add(&first)
	to.add(&second)
	drainStderr(t.Context(), strings.NewReader(strings.Join(transcript, "\n")+"\n"), &to)

	for i, got := range [][]string{first.lines, second.lines} {
		if !slices.Equal(got, transcript) {
			t.Errorf("observer %d saw\n%q\nwant\n%q", i, got, transcript)
		}
	}
}

// TestAMarkerOutlivesTheRingItUsedToLiveIn covers the retention window. A marker fires
// in the startup burst and the run then prints past the tail's capacity, which is
// ordinary for a two hour cast: the line scrolls out, and the verdict on the output
// must not scroll out with it.
func TestAMarkerOutlivesTheRingItUsedToLiveIn(t *testing.T) {
	lines := []string{"[mpegts @ 0x1] AAC bitstream not in ADTS format and extradata missing"}
	for i := range stderrTailCapacity * 2 {
		lines = append(lines, fmt.Sprintf("frame= %d fps=25 q=-1.0 size=1024kB", i))
	}

	tail, markers := drainLines(t, lines...)
	if err := markers.failure(); err == nil {
		t.Fatal("the marker was forgotten once its line left the retained tail")
	}
	if got := tail.snapshot(); slices.Contains(got, lines[0]) {
		t.Fatalf("the tail still holds the first line of %d, so this test proved nothing", len(lines))
	}
}

// TestEvidenceNamesTheMarkersWithoutRepeatingThem pins the value a classifier reads.
// ffmpeg prints these once per packet (188 times on the measured run), so a markers
// list that repeated would be a rule's input padded with one fact restated.
func TestEvidenceNamesTheMarkersWithoutRepeatingThem(t *testing.T) {
	marker := "AAC bitstream not in ADTS format and extradata missing"
	lines := healthyTranscript(slices.Repeat([]string{"[mpegts @ 0x1] " + marker}, 4)...)

	_, markers := drainLines(t, lines...)
	if got := markers.snapshot(); !slices.Equal(got, []string{marker}) {
		t.Errorf("markers = %q, want exactly one entry naming %q", got, marker)
	}
}

// TestEvidenceReportsNoStatusUntilThereIsOne covers the value stage 9's classifier
// keys on, over a real process. A rule reading a missing status as an exit code would
// convict every cast castor killed itself, which is every cast interrupted at the gate
// and every Ctrl+C.
func TestEvidenceReportsNoStatusUntilThereIsOne(t *testing.T) {
	ffmpegPath, _ := requireFFmpegTools(t)

	proc, err := Start(t.Context(), ffmpegPath,
		[]string{"-hide_banner", "-f", "lavfi", "-i", "testsrc=size=64x64:rate=5:duration=1", "-f", "null", "-"})
	if err != nil {
		t.Fatal(err)
	}
	if got := proc.Evidence().ExitStatus; got != noExitStatus {
		t.Errorf("exit status before Wait = %d, want %d: nothing has exited yet", got, noExitStatus)
	}
	if _, err := io.Copy(io.Discard, proc.Stdout); err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("the fixture encode failed: %v\n%q", err, proc.StderrTail())
	}

	ev := proc.Evidence()
	if ev.ExitStatus != 0 {
		t.Errorf("exit status = %d, want 0", ev.ExitStatus)
	}
	if len(ev.Lines) == 0 {
		t.Error("no stderr retained: the evidence a human reads when no rule recognised the failure")
	}
	if len(ev.Markers) != 0 {
		t.Errorf("markers = %q on a clean encode", ev.Markers)
	}
}

// TestTheExtraPipesCarryWhatTheFlagsSay drives a real pull against a real origin and
// reads all three of its outputs. It is the one test that can fail when the fd numbers
// and the readers stop agreeing, and it is here because the failure mode is silent in
// the worst way: transposing the two sends PCM samples to the progress parser (which
// finds no key=value lines and reports nothing, so a cast simply has no telemetry) and
// sends progress text to whisper (which transcribes noise). Nothing else in the suite
// would notice either.
func TestTheExtraPipesCarryWhatTheFlagsSay(t *testing.T) {
	ffmpegPath, _ := requireFFmpegTools(t)
	fixture := generateFixture(t, ffmpegPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, fixture)
	}))
	defer srv.Close()

	policy, err := read.For(read.Shape{}, testReadDeadline)
	if err != nil {
		t.Fatal(err)
	}
	opts := PullOptions{
		Source:        NetworkSource{URL: mustURL(t, srv.URL+"/video.mp4"), ContentType: media.MP4, Read: policy},
		PCM:           true,
		PCMSampleRate: 16000,
	}
	if got := opts.ExtraPipes(); got != 2 {
		t.Fatalf("a pull teeing PCM needs %d extra pipes, want 2", got)
	}

	proc, err := Start(t.Context(), ffmpegPath, PullArgs(opts), WithExtraPipes(opts.ExtraPipes()))
	if err != nil {
		t.Fatal(err)
	}

	var (
		wg       sync.WaitGroup
		spooled  int64
		pcmBytes int64
		samples  []media.Progress
	)
	wg.Go(func() { spooled, _ = io.Copy(io.Discard, proc.Stdout) })
	wg.Go(func() { pcmBytes, _ = io.Copy(io.Discard, proc.PCMFeed()) })
	wg.Go(func() {
		WatchProgress(proc.ProgressFeed(), func(s media.Progress) { samples = append(samples, s) })
	})
	// Every read finishes before Wait: os/exec closes the stdout pipe as soon as it
	// sees the process exit, so waiting first can truncate the very output being
	// measured.
	wg.Wait()
	if err := proc.Wait(); err != nil {
		t.Fatalf("the pull failed: %v\n%q", err, proc.StderrTail())
	}

	if spooled == 0 {
		t.Error("the spool output produced nothing, so this pull proves nothing about the other two")
	}
	// One second of mono s16le at 16 kHz is 32000 bytes. Asserting the order of
	// magnitude is what distinguishes real audio from a few hundred bytes of progress
	// text arriving on the wrong pipe.
	if pcmBytes < 16000 {
		t.Errorf("PCM feed delivered %d bytes; one second of mono s16le at 16 kHz is 32000", pcmBytes)
	}
	if len(samples) == 0 {
		t.Fatal("no progress samples: nothing parseable arrived on the progress pipe")
	}
	last := samples[len(samples)-1]
	if last.Position <= 0 || last.Speed <= 0 || last.Bytes <= 0 {
		t.Errorf("final sample = %+v; a completed read reports a position, a size and a speed", last)
	}
}

// collector is an observer that keeps everything, so a fan-out claim is about the
// lines themselves rather than about a verdict something reached from them.
type collector struct{ lines []string }

func (c *collector) Observe(line string) { c.lines = append(c.lines, line) }

// healthyTranscript is the ordinary startup burst a copy emits, optionally with
// one poisoned line spliced into it, so a marker has to be found among real
// output rather than on its own.
func healthyTranscript(extra ...string) []string {
	lines := []string{
		"Input #0, mpegts, from 'pipe:0':",
		"  Duration: N/A, start: 1.400000, bitrate: N/A",
		"  Stream #0:0[0x100]: Video: h264 (High), yuv420p(progressive), 320x240, 15 fps",
		"  Stream #0:1[0x101]: Audio: aac (LC), 44100 Hz, stereo, fltp",
		"Output #0, mp4, to 'pipe:1':",
	}
	lines = append(lines, extra...)
	return append(lines, "frame=  250 fps=0.0 q=-1.0 Lsize=     886kB")
}
