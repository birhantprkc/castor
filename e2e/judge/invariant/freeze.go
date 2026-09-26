package invariant

import (
	"fmt"
	"time"

	"github.com/stupside/castor/e2e/judge"
)

// freezeSlack is the longest a picture may hold before a viewer sees it stop, well above any real frame rate.
const freezeSlack = 500 * time.Millisecond

// Unfrozen holds that the picture never stops mid-cast: a segment lost or decoded corrupt leaves a hole in its timestamps.
type Unfrozen struct{}

func (Unfrozen) Name() string { return "freeze" }

func (Unfrozen) Judge(e judge.Evidence) []string {
	p := e.Received.Played
	return judge.FailIf(p.LongestFreeze > freezeSlack, fmt.Sprintf("the picture stops for %v mid-cast", p.LongestFreeze))
}
