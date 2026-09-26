package invariant

import (
	"fmt"
	"slices"

	"github.com/stupside/castor/e2e/judge"
)

// interlacedOrders are ffprobe's field orders that flag a picture interlaced.
var interlacedOrders = []string{"tt", "bb", "tb", "bt"}

// Progressive holds that a receiver which does not deinterlace gets a progressive picture, and that no picture lands combed.
type Progressive struct{}

func (Progressive) Name() string { return "progressive" }

func (Progressive) Judge(e judge.Evidence) []string {
	p := e.Received.Played
	if p.VideoPackets == 0 {
		return nil
	}
	flagged := slices.Contains(interlacedOrders, p.FieldOrder)
	return slices.Concat(
		judge.FailIf(flagged && !e.Endpoint.Plays.Deinterlaces, fmt.Sprintf("an interlaced picture (%s) reached a receiver that does not deinterlace", p.FieldOrder)),
		// Combing under a progressive flag shows on any receiver, since none deinterlaces what claims to be progressive.
		judge.FailIf(!flagged && e.Origin.Stream.Interlaced && p.Combed, "the picture lands combed: interlaced fields were re-encoded without a deinterlacer"),
	)
}
