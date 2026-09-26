package expect

import (
	"cmp"
	"fmt"

	"github.com/stupside/castor/e2e/judge"
)

// copied reads same codec, height, depth and chroma as a copy; a same-shape re-encode would pass for one.
func copied(e judge.Evidence) bool {
	p, s := e.Received.Played, e.Origin.Stream
	rung, _ := s.Rung(e.Ceiling)
	return p.Video == s.Video.Name() && p.Height == rung && p.Depth == s.Depth && p.Chroma == cmp.Or(s.Chroma, 420)
}

// Copied holds that the picture reached the receiver as the source published it.
type Copied struct{}

func (Copied) Name() string { return "copied" }

func (Copied) Judge(e judge.Evidence) []string {
	p, s := e.Received.Played, e.Origin.Stream
	return judge.FailIf(!copied(e), fmt.Sprintf("video was re-encoded (%s %dp %d-bit from %s %v %d-bit), want it copied", p.Video, p.Height, p.Depth, s.Video.Name(), s.Heights, s.Depth))
}
