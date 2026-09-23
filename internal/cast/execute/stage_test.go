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

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/engine/deliver"
	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/policy/compose"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// fakeStage is a burn-in without whisper; production needs a transcription model and cgo.
type fakeStage struct {
	burnIn string
	lead   *fakeLead

	attached atomic.Int64
	drained  atomic.Int64
	samples  atomic.Int64
	leadAsks atomic.Int64
}

func (s *fakeStage) Run(_ context.Context, pcm io.ReadCloser) {
	s.attached.Add(1)
	if pcm == nil {
		return
	}
	defer pcm.Close()
	n, _ := io.Copy(io.Discard, pcm)
	s.drained.Add(n)
}

func (s *fakeStage) Inputs() (string, error) { return s.burnIn, nil }

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
	c := bufferedCast(t, ffmpegPath, ffprobePath)
	c.burn = &fakeStage{burnIn: cuePath}

	if err := c.encode(t.Context()); err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	cmd, err := ffmpeg.EncodeArgs(c.opts)
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

	c := bufferedCast(t, ffmpegPath, ffprobePath)
	if _, err := c.spool.Write(program); err != nil {
		t.Fatal(err)
	}
	// A source nothing can measure: nothing is listening on that port.
	c.attempt.Program = programFromStream(t, &source.Candidate{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1", Path: "/gone.mp4"}, ContentType: media.MP4})

	if err := c.encode(t.Context()); err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	if c.opts.Video.Name() != "copy" || c.opts.Probe.VideoCodec != media.CodecH264 {
		t.Errorf("video %q from %+v, want a copy decided from the buffer's own h264", c.opts.Video.Name(), c.opts.Probe)
	}
}

// TestASilentSourceRunsNoStageAtAll: a PCM tee of a source with no audio maps nothing and ffmpeg refuses.
func TestASilentSourceRunsNoStageAtAll(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveSilentFixture(t, ffmpegPath)

	g, ctx := errgroup.WithContext(t.Context())
	program := programFromStream(t, origin.stream())
	cfg := castConfig(pushOnly(), ffmpegPath, ffprobePath)
	cfg.Subtitles = staging(&fakeStage{})
	c := &cast{
		cfg:     cfg,
		row:     compose.Row{Kind: compose.ReadOnce},
		attempt: attempt.Attempt{Program: program, Read: sourceReadPlan(t, program, testReadDeadline)},
		workDir: t.TempDir(),
		group:   g,
	}

	if err := c.read(ctx); err != nil {
		t.Fatalf("starting the read: %v", err)
	}
	if err := c.transcribe(ctx); err != nil {
		t.Fatalf("attaching the stage: %v", err)
	}
	<-c.reader.Done()
	if err := c.reader.Err(); err != nil {
		t.Fatalf("the read of a silent source failed: %v\n%q", err, c.reader.Evidence())
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	if c.burn != nil || c.reader.pcm != nil {
		t.Error("a silent source was given a burn-in or a PCM tee")
	}
	if c.spool.Size() == 0 {
		t.Error("the buffer is empty, so nothing was read at all")
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
	out := newExecutorAt(castConfig(pushOnly(), ffmpegPath, ffprobePath), connectTo(dev), staging(stage), "127.0.0.1").
		Run(ctx, attempt.Attempt{Try: 1, Program: program, Read: sourceReadPlan(t, program, testReadDeadline)})
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
func bufferedCast(t *testing.T, ffmpegPath, ffprobePath string) *cast {
	t.Helper()
	sp, err := deliver.NewSpool(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	return &cast{
		cfg:     castConfig(pushOnly(), ffmpegPath, ffprobePath),
		row:     compose.Row{Kind: compose.ReadOnce},
		dev:     &fakeDevice{caps: dlnaLike()},
		spool:   sp,
		workDir: t.TempDir(),
	}
}
