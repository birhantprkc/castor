package pipeline

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/deliver/spool"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/subtitle/cue"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// TestTheCueFileHoldsTheLineForTheFrameBeingEncoded covers the burn-in mechanism at
// the seam the encoder's telemetry moved across: the writer now places cues from
// -progress SAMPLES rather than from the feed itself, and drawtext reads the file it
// writes once per frame.
//
// It asserts the lookup, not just that something was written. The sample position is
// the encoder's MUX position and the frames being drawn are an encoder lookahead
// ahead of it, so a writer that looked up the cue at the position it was handed would
// place every line late (see cueLeadBias): the first sample here lands inside the cue
// only because of the bias.
func TestTheCueFileHoldsTheLineForTheFrameBeingEncoded(t *testing.T) {
	path, cues := cueFixture(t)
	write := cueWriter(t.Context(), path, cues, func() float64 { return 10 })

	// Mux position 1.5s, so the frame being drawn is around 2.5s, which is inside the
	// committed cue (2.0 to 4.0, trimmed inward to hug the audio).
	write(media.Progress{Position: 1500 * time.Millisecond, Speed: 1.15})
	if got := readFile(t, path); got != "Hello." {
		t.Errorf("cue file = %q, want the line covering the frame being encoded", got)
	}

	// Past the cue, the file must go empty rather than keep the last line on screen.
	write(media.Progress{Position: 5 * time.Second, Speed: 1.15})
	if got := readFile(t, path); got != "" {
		t.Errorf("cue file = %q after the cue ended, want it cleared", got)
	}

	// The swap is a rename, so nothing partial is ever visible at the path drawtext
	// re-opens; a leftover temp file means the writer wrote in place instead.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temp file survived the swap, so the update was not a rename")
	}
}

// TestAnUnchangedLineIsNotRewritten pins the other half of that: at ten samples a
// second, rewriting the same line every time is a rename per tick for a file ffmpeg
// re-opens per frame. The file is removed after the first swap, so a second write
// would recreate it.
func TestAnUnchangedLineIsNotRewritten(t *testing.T) {
	path, cues := cueFixture(t)
	write := cueWriter(t.Context(), path, cues, func() float64 { return 10 })

	sample := media.Progress{Position: 1500 * time.Millisecond}
	write(sample)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(sample)
	if _, err := os.Stat(path); err == nil {
		t.Error("the same line was written twice")
	}
}

// fakeStage is a cast's optional work with no whisper behind it, which is what makes the
// wiring testable at all: the production stage needs a transcription model and a cgo build, so
// the path that carries its cue file into the encode had no test on either side of it.
type fakeStage struct {
	burnIn string
	err    error
	lead   watch.Lead

	attached atomic.Int64
	samples  atomic.Int64
}

func (s *fakeStage) Attach(ctx context.Context, g *errgroup.Group, pcm io.ReadCloser) {
	s.attached.Add(1)
	g.Go(func() error {
		defer pcm.Close()
		_, _ = io.Copy(io.Discard, pcm)
		return nil
	})
}

func (s *fakeStage) Inputs() (string, error) { return s.burnIn, s.err }

func (s *fakeStage) Follow(context.Context) func(media.Progress) {
	return func(media.Progress) { s.samples.Add(1) }
}

func (s *fakeStage) Lead() watch.Lead { return s.lead }

// TestAStagesInputsReachTheEncodeThatDrawsThem covers the ordering hazard this port exists to
// remove. A burn-in set on an encode AFTER the copy-vs-encode decision produced a cast that
// played with no subtitles and no error anywhere, so the cue file has to be an INPUT to that
// decision, and the file has to exist before ffmpeg starts (drawtext re-opens it before every
// frame and ffmpeg dies on a read that fails).
//
// The assertion is on the command line, because that is the only place the two facts meet:
// the filter that draws the cues names the file the stage created.
func TestAStagesInputsReachTheEncodeThatDrawsThem(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	cuePath, _ := cueFixture(t)
	c, buffer := bufferedCast(t, ffmpegPath, ffprobePath)

	opts, err := c.bufferedEncode(t.Context(), dlnaLike(), buffer, stages{&fakeStage{burnIn: cuePath}})
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	args, err := ffmpeg.EncodeArgs(opts)
	if err != nil {
		t.Fatalf("building the args: %v", err)
	}
	if !slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, cuePath) }) {
		t.Errorf("no argument names the cue file the stage prepared, so this cast plays with nothing drawn on it: %v", args)
	}

	// And a cast with no stages draws nothing, so a subtitle-less cast is not paying for a
	// re-encode it has no cues for.
	opts, err = c.bufferedEncode(t.Context(), dlnaLike(), buffer, nil)
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	args, err = ffmpeg.EncodeArgs(opts)
	if err != nil {
		t.Fatalf("building the args: %v", err)
	}
	if slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, "drawtext") }) {
		t.Errorf("a cast with no stages was given a drawtext filter: %v", args)
	}
}

// TestTheBufferedEncodeIsMeasuredFromTheBufferAndNotTheSource covers the distinction that is
// load-bearing rather than incidental. The MPEG-TS buffer re-frames everything that passes
// through it (an AAC track that arrived as fMP4 comes out the far side as ADTS) and its carriage
// rules may have re-encoded an axis on the way in, so a decision taken from the original source
// is a decision about a stream nobody is reading.
//
// The source here answers nothing at all, so measuring it would leave nothing known and force a
// re-encode; the buffer holds an h264/AAC program this renderer decodes, so measuring it copies.
func TestTheBufferedEncodeIsMeasuredFromTheBufferAndNotTheSource(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	buffer := generateFixture(t, ffmpegPath, "buffer.ts", 1,
		[]string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-shortest", "-f", "mpegts"})

	c, _ := bufferedCast(t, ffmpegPath, ffprobePath)
	// A source nothing can measure: nothing is listening on that port.
	c.attempt.Source = &media.Stream{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1", Path: "/gone.mp4"}, ContentType: media.MP4}

	opts, err := c.bufferedEncode(t.Context(), dlnaLike(), buffer, nil)
	if err != nil {
		t.Fatalf("building the encode: %v", err)
	}
	if opts.Video.Name() != "copy" {
		t.Errorf("video = %q, want a copy: the buffer holds h264 this renderer decodes, and it is the buffer this encode reads", opts.Video.Name())
	}
	if opts.Probe.VideoCodec != media.CodecH264 {
		t.Errorf("the encode was built from %+v, which is not a measurement of the buffer", opts.Probe)
	}
}

// TestAStageThatCannotPrepareItsInputsStopsTheCast covers the other half: the file it hands
// back must exist, so a stage that could not create it has to be heard rather than answered
// with an empty path. An encode carrying a path drawtext cannot open dies on its first frame.
func TestAStageThatCannotPrepareItsInputsStopsTheCast(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	c, buffer := bufferedCast(t, ffmpegPath, ffprobePath)

	failing := &fakeStage{err: errors.New("creating subtitle cue file: read-only file system")}
	if _, err := c.bufferedEncode(t.Context(), dlnaLike(), buffer, stages{failing}); err == nil {
		t.Error("a stage that could not prepare its inputs was ignored")
	}
}

// TestEveryStageIsStartedOverTheReadsAudioFeed pins the other end of the feed the read is told
// to tee. A stage that is never started is a cast that tees audio into a pipe nobody reads,
// which is not a waste but a stall: the pipe is unbuffered, so it blocks the download.
func TestEveryStageIsStartedOverTheReadsAudioFeed(t *testing.T) {
	first, second := &fakeStage{}, &fakeStage{}
	pcm, feed := io.Pipe()
	g, ctx := errgroup.WithContext(t.Context())

	stages{first, second}.attach(ctx, g, pcm)
	_ = feed.Close()
	if err := g.Wait(); err != nil {
		t.Fatalf("a stage reported %v while draining a feed that simply ended", err)
	}
	if first.attached.Load() != 1 || second.attached.Load() != 1 {
		t.Errorf("stages started %d and %d times, want each exactly once", first.attached.Load(), second.attached.Load())
	}
}

// TestEveryStageSeesTheEncodersSamples pins the fan-out, and the nil that is not a missing
// consumer: the delivery driver drains the encoder's report either way, because ffmpeg writes
// it with a blocking write and an unread feed stops the encode dead.
func TestEveryStageSeesTheEncodersSamples(t *testing.T) {
	first, second := &fakeStage{}, &fakeStage{}
	follow := stages{first, second}.follow(t.Context())
	if follow == nil {
		t.Fatal("a cast with stages follows nothing")
	}
	follow(media.Progress{Position: time.Second})
	if first.samples.Load() != 1 || second.samples.Load() != 1 {
		t.Errorf("samples reached %d and %d stages, want both", first.samples.Load(), second.samples.Load())
	}

	if stages(nil).follow(t.Context()) != nil {
		t.Error("a cast with no stages was given a consumer of the encoder's samples anyway")
	}
}

// TestTheReadinessLeadIsAStagesOwn covers what the readiness rules ask of a cast: nil is how
// they are told a transcription lead is no part of being playable here, and a typed nil would
// answer "yes, and it has committed nothing", forever.
func TestTheReadinessLeadIsAStagesOwn(t *testing.T) {
	if stages(nil).lead() != nil {
		t.Error("a cast with no stages reports a lead to wait on")
	}
	if (stages{&fakeStage{}}).lead() != nil {
		t.Error("a stage with nothing to wait for reports a lead anyway")
	}
	lead := fakeLead{}
	if got := (stages{&fakeStage{}, &fakeStage{lead: lead}}).lead(); got != watch.Lead(lead) {
		t.Errorf("lead = %v, want the one stage that has one", got)
	}
}

// TestTheReadTeesAudioExactlyWhenAStageWantsIt pins the one fact the read has to be told before
// it starts, long before a renderer has answered anything. Teeing a feed nobody reads is not a
// waste: the pipe is unbuffered, so it blocks the download and the cast never becomes playable.
func TestTheReadTeesAudioExactlyWhenAStageWantsIt(t *testing.T) {
	if stages(nil).wantPCM() {
		t.Error("a cast with no stages asks the read to tee audio nobody will read")
	}
	if !(stages{&fakeStage{}}).wantPCM() {
		t.Error("a cast with a stage is not given the audio feed it exists to consume")
	}
}

// bufferedCast is the read-once composition's material at the point its encode is built: an
// empty buffer (so the measurement finds nothing, which is the ordinary early-cast case and
// leaves nothing known against it) and this host's real tools.
func bufferedCast(t *testing.T, ffmpegPath, ffprobePath string) (*cast, string) {
	t.Helper()
	sp, err := spool.New(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := castConfig(device.TypeDLNA, ffmpegPath, ffprobePath)
	return &cast{cfg: cfg, policy: core.CopyWhatFits, workDir: t.TempDir()}, sp.Path()
}

// cueFixture is an existing (empty) cue file and one committed cue running from 2.0 to
// 4.0 seconds. The file exists because drawtext opens it before every frame and ffmpeg
// dies on a read that fails, which is why the production path creates it before the
// encoder starts.
func cueFixture(t *testing.T) (string, *cue.Builder) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cue.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cues := cue.NewBuilder()
	cues.Commit([]cue.Word{{Start: 2, End: 4, Text: "Hello."}}, 10)
	return path, cues
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
