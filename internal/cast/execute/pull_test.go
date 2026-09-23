package execute

import (
	"testing"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/policy/compose"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
)

// TestTheReadsFloorEncodeIsCappedAtTheCastsCeiling: an uncapped read encode falls below realtime.
func TestTheReadsFloorEncodeIsCappedAtTheCastsCeiling(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)

	// The fixture is 240p; the ceiling sits under it.
	cfg := castConfig(pushOnly(), ffmpegPath, ffprobePath)
	cfg.MaxHeight = 120
	cfg.Subtitles = noStage
	program := programFromStream(t, origin.stream())

	g, ctx := errgroup.WithContext(t.Context())
	c := &cast{
		cfg:     cfg,
		row:     compose.Row{Kind: compose.ReadOnce},
		attempt: attempt.Attempt{Program: program, Read: sourceReadPlan(t, program, testReadDeadline), Decode: media.Axes{Video: true}},
		workDir: t.TempDir(),
		group:   g,
	}
	if err := c.read(ctx); err != nil {
		t.Fatal(err)
	}
	<-c.reader.Done()
	if err := c.reader.Err(); err != nil {
		t.Fatalf("the read failed: %v\n%q", err, c.reader.Evidence())
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}

	info, _, err := probe.FFprobe(ffprobePath).File(c.spool.Path()).Probe(t.Context())
	if err != nil {
		t.Fatalf("probing the buffer this read wrote: %v", err)
	}
	if info.VideoHeight != 120 {
		t.Errorf("the read produced a %dp picture, want the 120p ceiling", info.VideoHeight)
	}
}
