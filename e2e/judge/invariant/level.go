package invariant

import (
	"fmt"

	"github.com/stupside/castor/e2e/judge"
)

// Level holds that the picture lands at a codec level the receiver decodes.
type Level struct{}

func (Level) Name() string { return "level" }

func (Level) Judge(e judge.Evidence) []string {
	p := e.Received.Played
	if p.VideoPackets == 0 {
		return nil
	}
	ceiling := e.Endpoint.Plays.Levels[p.Video]
	return judge.FailIf(ceiling > 0 && p.Level > ceiling, fmt.Sprintf("%s level %d landed, the receiver decodes up to %d", p.Video, p.Level, ceiling))
}
