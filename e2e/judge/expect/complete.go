package expect

import (
	"fmt"
	"time"

	"github.com/stupside/castor/e2e/judge"
)

// Complete holds a cast watched to the end to landing every frame the source has, give or take a segment.
type Complete struct{}

func (Complete) Name() string { return "complete" }

func (Complete) Judge(e judge.Evidence) []string {
	if e.Viewer.WillStop() {
		return nil
	}
	s, landed := e.Origin.Stream, e.Received.Played.VideoPackets
	source := time.Duration(s.Seconds) * time.Second
	want := int((source - judge.DurationSlack).Seconds() * s.FPS())
	return judge.FailIf(landed < want, fmt.Sprintf("played %d of %d frames, want at least %d", landed, int(source.Seconds()*s.FPS()), want))
}
