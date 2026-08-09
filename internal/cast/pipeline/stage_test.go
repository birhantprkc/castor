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

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/deliver/spool"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// fakeStage is a cast's optional work with no whisper behind it, which is what makes the
// wiring testable at all: the production stage needs a transcription model and a cgo build, so
// every site that feeds one had to be driven from a fake.
type fakeStage struct {
	burnIn string
	err    error
	lead   watch.Lead

	attached atomic.Int64
	drained  atomic.Int64
	samples  atomic.Int64
	leadAsks atomic.Int64
}

func (s *fakeStage) Attach(ctx context.Context, g *errgroup.Group, pcm io.ReadCloser) {
	s.attached.Add(1)
	g.Go(func() error {
		// A stage started over no feed is named rather than dereferenced: the read has to be
		// told to tee before it starts, so getting that wrong is a cast whose stage hears
		// nothing at all, and it is worth a sentence rather than a nil-pointer stack.
		if pcm == nil {
			return errors.New("the stage was started over no audio feed, so the read was never told to tee one")
		}
		defer pcm.Close()
		n, _ := io.Copy(io.Discard, pcm)
		s.drained.Add(n)
		return nil
	})
}

func (s *fakeStage) Inputs() (string, error) { return s.burnIn, s.err }

func (s *fakeStage) Follow(context.Context) func(media.Progress) {
	return func(media.Progress) { s.samples.Add(1) }
}

func (s *fakeStage) Lead() watch.Lead {
	s.leadAsks.Add(1)
	return s.lead
}

// noStage is what every cast in this suite but the two below runs: nothing beside the read,
// which is also the shipping default (a burn-in is opt-in configuration).
func noStage(context.Context, core.Config, string) Stage { return nil }

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

	cuePath := existingCueFile(t)
	c, buffer := bufferedCast(t, ffmpegPath, ffprobePath)

	opts, err := c.bufferedEncode(t.Context(), dlnaLike(), buffer, &fakeStage{burnIn: cuePath})
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

	// And a cast with no stage draws nothing, so a subtitle-less cast is not paying for a
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
		t.Errorf("a cast with no stage was given a drawtext filter: %v", args)
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
	if _, err := c.bufferedEncode(t.Context(), dlnaLike(), buffer, failing); err == nil {
		t.Error("a stage that could not prepare its inputs was ignored")
	}
}

// TestTheReadTeesAudioExactlyWhenAStageWantsIt pins the one fact the read has to be told before
// it starts, long before a renderer has answered anything, over a real read of a real source.
//
// Both directions cost a cast. A stage that is never handed the feed transcribes silence; a
// feed nobody drains is not a waste but a stall, because the pipe is unbuffered and
// backpressure on it throttles the whole download (which then reads as a starving link, since
// the pace this read may be judged against is withheld the moment it tees).
func TestTheReadTeesAudioExactlyWhenAStageWantsIt(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)

	for _, tt := range []struct {
		name  string
		stage *fakeStage
	}{
		{name: "a cast that runs a stage", stage: &fakeStage{}},
		{name: "a cast that runs none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			g, readCtx := errgroup.WithContext(ctx)
			c := &cast{
				cfg:     castConfig(device.TypeDLNA, ffmpegPath, ffprobePath),
				attempt: attempt.Attempt{Source: origin.stream(), Read: sourcePolicy(t, 30*time.Second)},
				workDir: t.TempDir(),
				group:   g,
			}

			// A nil *fakeStage would be a Stage that exists, which is the case this test is
			// distinguishing, so the interface is only ever given a stage there really is.
			var stage Stage
			if tt.stage != nil {
				stage = tt.stage
			}
			_, pl, err := c.startReading(readCtx, stage)
			if err != nil {
				t.Fatalf("starting the read: %v", err)
			}
			defer func() {
				cancel()
				_ = g.Wait()
			}()

			if teed := pl.pcm != nil; teed != (tt.stage != nil) {
				t.Errorf("the read tees audio = %v for %s", teed, tt.name)
			}
			if tt.stage == nil {
				return
			}
			if got := tt.stage.attached.Load(); got != 1 {
				t.Errorf("the stage was started %d times over the feed the read was told to tee, want exactly once", got)
			}
		})
	}
}

// TestEveryPartOfAStageIsWiredIntoTheCastThatRunsIt drives a whole read-once cast with a stage
// in it, which is the property no unit over the port can state: each of the four things a stage
// is asked for is asked at a different point in the leg, and a stage the leg never asks is a
// cast that plays with nothing drawn on it and no error anywhere.
//
// The lead is asked for before the gate (a cast whose transcription has not reached the frame
// being encoded must be held), the feed is teed and drained by the read, and the encoder's
// samples are what place the cues, so all four are asserted from the stage's own side.
func TestEveryPartOfAStageIsWiredIntoTheCastThatRunsIt(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)

	// A finished transcription, which is how a short source clears the readiness rule: it has
	// nothing left to commit, so the gate has nothing left to hold for.
	stage := &fakeStage{lead: fakeLead{done: true}}
	dev := &fakeDevice{caps: dlnaLike(), drain: true}

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()
	if err := castWith(ctx, t, castConfig(device.TypeDLNA, ffmpegPath, ffprobePath), connectTo(dev), origin.stream(), stage); err != nil {
		t.Fatalf("casting with a stage: %v", err)
	}

	if got := stage.attached.Load(); got != 1 {
		t.Errorf("the stage was started %d times, want exactly once", got)
	}
	if got := stage.drained.Load(); got == 0 {
		t.Error("the stage was handed no audio at all, so a transcription of this cast would have had nothing to hear")
	}
	if got := stage.leadAsks.Load(); got == 0 {
		t.Error("nothing asked the stage how far it had committed, so the gate cannot be holding for it: this is the cast that ships a picture with nothing drawn on it")
	}
	if got := stage.samples.Load(); got == 0 {
		t.Error("no sample of the encoder reached the stage, so nothing would place a cue against the frame being encoded")
	}
}

// castWith drives one attempt through the executor with the stage that cast is to run. The
// stage is injected exactly as production injects it (see cast.burnInStage), which is what lets
// the whole leg be driven here without a whisper model or a cgo build.
func castWith(ctx context.Context, t *testing.T, cfg core.Config, connect ConnectFunc, source *media.Stream, stage Stage) error {
	t.Helper()
	return NewExecutor(cfg, connect, func(context.Context, core.Config, string) Stage { return stage }, "127.0.0.1").
		Run(ctx, attempt.Attempt{Try: 1, Source: source, Read: sourcePolicy(t, 30*time.Second)}).Err
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

// existingCueFile is the file a stage hands back from Inputs: it exists, because drawtext opens
// it before every frame and ffmpeg dies on a read that fails.
func existingCueFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cue.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
