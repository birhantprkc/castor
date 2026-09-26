package expect

import (
	"fmt"

	"github.com/stupside/castor/e2e/judge"
)

// Encoded holds that castor re-encoded the picture.
type Encoded struct{}

func (Encoded) Name() string { return "encoded" }

func (Encoded) Judge(e judge.Evidence) []string {
	p := e.Received.Played
	return judge.FailIf(copied(e), fmt.Sprintf("video was copied as %s %dp, want it encoded", p.Video, p.Height))
}
