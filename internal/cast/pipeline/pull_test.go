package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/deliver/spool"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/read"
)

// TestTheReadReportsWhatItIsDelivering drives a real pull against a real origin and
// asks it what it delivered. Before this the reader produced no telemetry at all: the
// only number a cast had was the spool's size, so a link that moved 14.4 MB and then
// stopped printed the same two fields as one that had finished, and the speed the read
// was achieving against the speed it was allowed could not be formed at all.
//
// It asserts all three fields of the sample because they answer different questions:
// bytes separate "nothing is arriving" from "something is", and position with speed
// separate "arriving" from "arriving too slowly to watch".
func TestTheReadReportsWhatItIsDelivering(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)
	cfg := castConfig("", ffmpegPath, ffprobePath)

	sp, err := spool.New(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := read.For(read.Shape{}, cfg.Transcode.RWTimeout)
	if err != nil {
		t.Fatal(err)
	}

	pl, err := startPull(t.Context(), cfg.Transcode, origin.stream(), policy, sp, carriage.Axes{}, cfg.Resolver.MaxHeight, false)
	if err != nil {
		t.Fatal(err)
	}
	<-pl.Done()
	if err := pl.Err(); err != nil {
		t.Fatalf("the pull failed: %v\n%q", err, pl.Evidence())
	}

	// The feed is parsed by its own goroutine, so the last block ffmpeg wrote before
	// exiting can still be in the pipe when the download settles. Waiting for the
	// sample rather than reading once is what makes that ordering irrelevant.
	sample := pl.Progress()
	for deadline := time.Now().Add(5 * time.Second); sample.Speed == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		sample = pl.Progress()
	}

	if sample.Speed <= 0 {
		t.Fatalf("the read reported no speed (sample %+v); the one measure of deliverability that needs no declared bitrate", sample)
	}
	if sample.Position <= 0 {
		t.Errorf("the read reported no media position (sample %+v)", sample)
	}
	if sample.Bytes <= 0 {
		t.Errorf("the read reported no size (sample %+v)", sample)
	}
	// The whole fixture is one second of media, so a read that reported more than it
	// could have produced is reporting the wrong output.
	if sample.Position > 2*time.Second {
		t.Errorf("position = %s from a one second fixture", sample.Position)
	}
}

// TestTheReadsFloorEncodeIsCappedAtTheCastsCeiling drives the read that PRODUCES a picture
// rather than copying one, and asks the buffer it wrote how tall that picture is.
//
// The recovery for a copy that broke upstream (and the buffer container refusing a codec)
// is this encode, and uncapped it is libx264 veryfast at the SOURCE resolution. At
// 3840x2160 that is well under realtime on ordinary hardware, so the gate measures
// speed < 1.0, reports the SOURCE as too slow to watch, and abandons the very links the
// recovery was reaching for: the escape manufactures the verdict it exists to escape. The
// cap is the ceiling this cast is already committed to, and the encode downstream of this
// buffer scales to the same number anyway, so the pixels cost nothing that would have been
// kept.
//
// It runs through the leg's own startReading rather than the reader alone, because the
// ceiling has to REACH the read: the configured height is what the cast is committed to,
// and it is one argument away from being left behind.
//
// The assertion is a ratio and not a 4K run: what is being pinned is that the ceiling binds
// the picture this read produces, and a 2160p fixture would spend a minute of test time
// proving the same thing. The axis is asked for the way the recovery asks for it, as the
// attempt's Decode, which is the path that manufactured the verdict.
func TestTheReadsFloorEncodeIsCappedAtTheCastsCeiling(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)

	// The fixture is 320x240, so a ceiling under it is the whole question, and no ceiling
	// is the zero-value convention core.Resolve uses for a cast with none.
	for _, tt := range []struct {
		name      string
		maxHeight int
		want      int
	}{
		{name: "held to the cast's ceiling", maxHeight: 120, want: 120},
		{name: "no ceiling keeps the source's own height", maxHeight: 0, want: 240},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := castConfig("", ffmpegPath, ffprobePath)
			cfg.Resolver.MaxHeight = tt.maxHeight
			policy, err := read.For(read.Shape{}, cfg.Transcode.RWTimeout)
			if err != nil {
				t.Fatal(err)
			}

			g, ctx := errgroup.WithContext(t.Context())
			c := &cast{
				cfg:     cfg,
				attempt: attempt.Attempt{Source: origin.stream(), Read: policy, Decode: carriage.Axes{Video: true}},
				workDir: t.TempDir(),
				group:   g,
			}
			sp, pl, err := c.startReading(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			<-pl.Done()
			if err := pl.Err(); err != nil {
				t.Fatalf("the read failed: %v\n%q", err, pl.Evidence())
			}
			if err := g.Wait(); err != nil {
				t.Fatal(err)
			}

			info, err := ffmpeg.FileProbe(ffprobePath, sp.Path()).Probe(t.Context())
			if err != nil {
				t.Fatalf("probing the buffer this read wrote: %v", err)
			}
			if info.VideoHeight != tt.want {
				t.Errorf("the read produced a %dp picture, want %dp: an encode nobody capped runs at the source's own resolution, which is what makes this read slower than playback",
					info.VideoHeight, tt.want)
			}
		})
	}
}

// TestAReadCastorStoppedReportsNoExitStatus is the sign convention a whole class of
// misattribution rests on.
//
// Castor kills the reader on every fault it names itself (a stall, a starving link, a
// cancelled cast), so its own error path never runs and it has no status to report. A
// classification that read "no status" as an exit would blame the copy for all of those and
// answer a stalled 4K link by re-encoding the whole title.
func TestAReadCastorStoppedReportsNoExitStatus(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)
	cfg := castConfig("", ffmpegPath, ffprobePath)

	sp, err := spool.New(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := read.For(read.Shape{}, cfg.Transcode.RWTimeout)
	if err != nil {
		t.Fatal(err)
	}

	// A read with no process yet answers the same way, since a judgement is reached at
	// whatever instant it is reached and this has to be total over the read's whole life.
	if got := (&pull{}).ExitStatus(); got >= 0 {
		t.Errorf("a read with no process reports exit status %d, want no status at all", got)
	}

	ctx, cancel := context.WithCancel(t.Context())
	pl, err := startPull(ctx, cfg.Transcode, origin.stream(), policy, sp, carriage.Axes{}, cfg.Resolver.MaxHeight, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := pl.ExitStatus(); got >= 0 {
		t.Errorf("a running read reports exit status %d, want no status: it has not exited", got)
	}
	cancel()
	<-pl.Done()

	if got := pl.ExitStatus(); got >= 0 {
		t.Errorf("a read castor killed reports exit status %d, want no status: it failed at nothing and its copy is not to blame", got)
	}
	// The axes are still reported, because what a read was doing is knowable whether or not
	// it got to exit: it is a term of the command line castor built.
	if got := pl.Copying(); !got.Video || !got.Audio {
		t.Errorf("the read says it was copying %s, want both halves: nothing asked it to produce either", got)
	}
}
