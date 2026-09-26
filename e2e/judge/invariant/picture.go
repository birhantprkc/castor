package invariant

import (
	"fmt"
	"slices"

	"github.com/stupside/castor/e2e/judge"
)

// PictureLanded holds that video reached the receiver in a codec it decodes, at the rung the ceiling selects, with no frame above it.
type PictureLanded struct{}

func (PictureLanded) Name() string { return "picture" }

func (PictureLanded) Judge(e judge.Evidence) []string {
	p, plays, s := e.Received.Played, e.Endpoint.Plays, e.Origin.Stream
	failures := slices.Concat(
		judge.FailIf(p.VideoPackets == 0, "no video packet landed"),
		judge.FailIf(!decodesVideo(plays.Video, p.Video, p.Depth), fmt.Sprintf("video %q at %d bits, but the receiver decodes %v", p.Video, p.Depth, plays.Video)),
		judge.FailIf(hdr[p.Transfer] && !plays.HDR, fmt.Sprintf("the picture is tagged %s, an HDR transfer the receiver never said it engages", p.Transfer)),
		judge.FailIf(p.Tallest > e.Ceiling, fmt.Sprintf("a frame is %dp, above the %dp ceiling", p.Tallest, e.Ceiling)),
	)
	// A quarter-turned source may land stood up, so its rendition is whichever side is shorter.
	height := p.Height
	if quarterTurn(s.Rotation) {
		height = min(p.Width, p.Height)
	}
	// The tallest rendition within the ceiling is what castor must pick; with none within it, anything no taller than it.
	if rung, ok := s.Rung(e.Ceiling); ok {
		return append(failures, judge.FailIf(height != rung, fmt.Sprintf("video is %dp, want the %dp rendition the ceiling selects from %v", height, rung, s.Heights))...)
	}
	return append(failures, judge.FailIf(height > e.Ceiling, fmt.Sprintf("video is %dp, above the %dp ceiling", height, e.Ceiling))...)
}

// hdr is every transfer that makes a picture HDR, as ffprobe names them.
var hdr = map[string]bool{"smpte2084": true, "arib-std-b67": true}

func decodesVideo(plays map[string]int, codec string, depth int) bool {
	deepest, ok := plays[codec]
	return ok && depth <= deepest
}
