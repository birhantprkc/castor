package execute

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// fakeStage is a burn-in without whisper; production needs a transcription model and cgo.
type fakeStage struct {
	burnIn string
	lead   *fakeLead

	// linger is how long Run keeps going once its feed ends, as a transcription flushing its last cues does.
	linger time.Duration

	attached atomic.Int64
	ended    atomic.Int64
	drained  atomic.Int64
	samples  atomic.Int64
	leadAsks atomic.Int64
}

func (s *fakeStage) Run(_ context.Context, pcm io.ReadCloser) {
	s.attached.Add(1)
	defer s.ended.Add(1)
	if pcm == nil {
		return
	}
	defer pcm.Close()
	n, _ := io.Copy(io.Discard, pcm)
	s.drained.Add(n)
	time.Sleep(s.linger)
}

func (s *fakeStage) Inputs() (string, error) { return s.burnIn, nil }

func (*fakeStage) SampleRate() int { return 16000 }

func (s *fakeStage) Follow(context.Context) func(media.Progress) {
	return func(media.Progress) { s.samples.Add(1) }
}

func (s *fakeStage) LatestEnd() float64 {
	s.leadAsks.Add(1)
	if s.lead == nil {
		return 0
	}
	return s.lead.latest
}

func (s *fakeStage) Done() bool { return s.lead == nil || s.lead.done }

// noStage is the shipping default: no burn-in.
func noStage(context.Context, string) Burn { return nil }

func staging(b Burn) Subtitles {
	return func(context.Context, string) Burn { return b }
}

// TestAStagesInputsReachTheEncodeThatDrawsThem: the cue must be an input before the copy decision.
func TestAStagesInputsReachTheEncodeThatDrawsThem(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	cuePath := filepath.Join(t.TempDir(), "cue.txt")
	if err := os.WriteFile(cuePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, buf := bufferedCast(t, ffmpegPath, ffprobePath)

	opts, err := s.encode(&fakeDevice{caps: dlnaLike()}, feed{buffered: buf}, &fakeStage{burnIn: cuePath})
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	cmd, err := transcode.EncodeArgs(opts)
	if err != nil {
		t.Fatalf("building the args: %v", err)
	}
	if !slices.ContainsFunc(cmd.Args, func(arg string) bool { return strings.Contains(arg, cuePath) }) {
		t.Errorf("no argument names the cue file the stage prepared: %v", cmd.Args)
	}
}

func TestTheBufferedEncodeIsMeasuredFromTheBufferAndNotTheSource(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	program, err := os.ReadFile(generateFixture(t, ffmpegPath, "buffer.ts", 1,
		[]string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-shortest", "-f", "mpegts"}))
	if err != nil {
		t.Fatal(err)
	}

	s, buf := bufferedCast(t, ffmpegPath, ffprobePath)
	if _, err := buf.reader.spool.Write(program); err != nil {
		t.Fatal(err)
	}
	// A source nothing can measure: nothing is listening on that port.
	s.attempt.Program = programFromStream(t, &source.Stream{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1", Path: "/gone.mp4"}, ContentType: media.MP4})

	opts, err := s.encode(&fakeDevice{caps: dlnaLike()}, feed{buffered: buf}, nil)
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	if opts.Video.Name() != "copy" || opts.Probe.VideoCodec != media.CodecH264 {
		t.Errorf("video %q from %+v, want a copy decided from the buffer's own h264", opts.Video.Name(), opts.Probe)
	}
}

// TestASilentSourceRunsNoStageAtAll: a PCM tee of a source with no audio maps nothing and ffmpeg refuses.
func TestASilentSourceRunsNoStageAtAll(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveSilentFixture(t, ffmpegPath)

	program := programFromStream(t, origin.stream())
	cfg := castConfig(pushOnly(), ffmpegPath, ffprobePath)
	cfg.Subtitles = staging(&fakeStage{})
	s := readingSession(t, cfg, attempt.Attempt{Program: program, Fetch: sourceReadPlan(t, program, testReadDeadline)})

	followed, err := s.follow(program)
	if err != nil {
		t.Fatal(err)
	}
	buf, err := s.read(workspace{dir: t.TempDir()}, followed)
	if err != nil {
		t.Fatalf("starting the read: %v", err)
	}
	<-buf.reader.Done()
	if err := buf.reader.Err(); err != nil {
		t.Fatalf("the read of a silent source failed: %v\n%q", err, buf.reader.Evidence())
	}
	if buf.burn != nil || buf.reader.pcm != nil {
		t.Error("a silent source was given a burn-in or a PCM tee")
	}
	if buf.reader.spool.Size() == 0 {
		t.Error("the buffer is empty, so nothing was read at all")
	}
}

// The transcription writes into the work dir, so releasing its read stops it before the dir goes.
func TestReleasingAReadStopsItsTranscriptionBeforeTheWorkDirGoes(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)

	stage := &fakeStage{linger: 200 * time.Millisecond}
	cfg := castConfig(pushOnly(), ffmpegPath, ffprobePath)
	cfg.Subtitles = staging(stage)
	program := programFromStream(t, origin.stream())
	s := readingSession(t, cfg, attempt.Attempt{Program: program, Fetch: sourceReadPlan(t, program, testReadDeadline)})

	// Stands for the workspace, acquired before the read and so released after it.
	var running int64
	s.releases.push(func() error { running = stage.attached.Load() - stage.ended.Load(); return nil })
	followed, err := s.follow(program)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.read(workspace{dir: t.TempDir()}, followed); err != nil {
		t.Fatalf("starting the read: %v", err)
	}
	if err := s.releases.release(); err != nil {
		t.Fatal(err)
	}
	if stage.attached.Load() != 1 || running != 0 {
		t.Errorf("the work dir went with %d transcription(s) of %d still running", running, stage.attached.Load())
	}
}

// A read released while its source still answers is stopped, not waited out to the source's end or deadline.
func TestReleasingAReadStopsItWhileItsSourceStillAnswers(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	stream := &source.Stream{URL: quietOrigin(t, programHead(t, ffmpegPath)), ContentType: media.MPEGTS}
	program := programFromStream(t, stream)
	s := readingSession(t, castConfig(pushOnly(), ffmpegPath, ffprobePath), attempt.Attempt{Program: program, Fetch: sourceReadPlan(t, program, time.Hour)})

	buf, err := s.read(workspace{dir: t.TempDir()}, program)
	if err != nil {
		t.Fatalf("starting the read: %v", err)
	}
	released := make(chan error, 1)
	go func() { released <- s.releases.release() }()
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("releasing a read of a source that keeps answering waited for the source instead of stopping the read")
	}
	if _, running := <-buf.reader.Done(); running {
		t.Error("the read was still running once released")
	}
}

// TestABurnInIsFedAndAwaitedByTheCastThatRunsIt drives a whole cast with a stage.
func TestABurnInIsFedAndAwaitedByTheCastThatRunsIt(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)

	stage := &fakeStage{lead: &fakeLead{done: true}}
	dev := &fakeDevice{caps: dlnaLike(), drain: true}

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()
	program := programFromStream(t, origin.stream())
	out := newExecutor(castConfig(pushOnly(), ffmpegPath, ffprobePath), connectTo(dev), staging(stage)).
		Run(ctx, attempt.Attempt{Try: 1, Program: program, Fetch: sourceReadPlan(t, program, testReadDeadline)})
	if out.Err != nil {
		t.Fatalf("casting with a stage: %v", out.Err)
	}

	if got := stage.attached.Load(); got != 1 {
		t.Errorf("the stage was started %d times, want exactly once", got)
	}
	if stage.drained.Load() == 0 {
		t.Error("the stage was handed no audio")
	}
	if stage.leadAsks.Load() == 0 {
		t.Error("nothing asked the stage how far it had committed, so the gate is not holding for it")
	}
	if stage.samples.Load() == 0 {
		t.Error("no sample of the encoder reached the stage")
	}
}

// bufferedCast is the read-once composition's material for building an encode (empty buffer).
func bufferedCast(t *testing.T, ffmpegPath, ffprobePath string) (*session, *buffered) {
	t.Helper()
	sp, err := deliver.NewSpool(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	s := readingSession(t, castConfig(pushOnly(), ffmpegPath, ffprobePath), attempt.Attempt{})
	return s, &buffered{reader: &pull{spool: sp, done: make(chan struct{})}}
}

// readingSession is an attempt's session, released when the test ends.
func readingSession(t *testing.T, cfg Config, a attempt.Attempt) *session {
	t.Helper()
	s := &session{cfg: cfg, attempt: a, ctx: t.Context(), releases: &releases{}}
	t.Cleanup(func() { _ = s.releases.release() })
	return s
}
