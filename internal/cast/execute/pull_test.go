package execute

import (
	"testing"

	"github.com/stupside/castor/internal/cast/attempt"
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

	s := readingSession(t, cfg, attempt.Attempt{Program: program, Fetch: sourceFetchPlan(t, program, testReadDeadline), Decode: media.Axes{Video: true}})
	followed, err := s.follow(program)
	if err != nil {
		t.Fatal(err)
	}
	buf, err := s.read(t.TempDir(), followed)
	if err != nil {
		t.Fatal(err)
	}
	<-buf.reader.Done()
	if err := buf.reader.Err(); err != nil {
		t.Fatalf("the read failed: %v\n%q", err, buf.reader.Evidence())
	}

	info, _, err := probe.FFprobe(ffprobePath).File(buf.reader.spool.Path()).Probe(t.Context())
	if err != nil {
		t.Fatalf("probing the buffer this read wrote: %v", err)
	}
	if info.VideoHeight != 120 {
		t.Errorf("the read produced a %dp picture, want the 120p ceiling", info.VideoHeight)
	}
}
