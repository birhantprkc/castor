package invariant

import (
	"fmt"
	"time"

	"github.com/stupside/castor/e2e/judge"
)

// syncSlack is how far sound may drift from where the source opens it: a few frames, well under what a viewer notices.
const syncSlack = 250 * time.Millisecond

// InSync holds that sound opens against the picture where the source opens it.
type InSync struct{}

func (InSync) Name() string { return "sync" }

func (InSync) Judge(e judge.Evidence) []string {
	p, delay := e.Received.Played, e.Origin.Stream.AudioDelay
	if p.AudioPackets == 0 || p.VideoPackets == 0 {
		return nil
	}
	gap := p.AudioStart - p.VideoStart
	return judge.FailIf((gap-delay).Abs() > syncSlack, fmt.Sprintf("audio opens %v after video, the source opens it %v after", gap, delay))
}
