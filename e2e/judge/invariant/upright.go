package invariant

import "github.com/stupside/castor/e2e/judge"

// Upright holds that the picture displays the way the source does, portrait or landscape.
type Upright struct{}

func (Upright) Name() string { return "upright" }

func (Upright) Judge(e judge.Evidence) []string {
	p := e.Received.Played
	if p.VideoPackets == 0 {
		return nil
	}
	// The source is coded 16:9, so only a quarter turn of its display matrix stands it up.
	source, landed := quarterTurn(e.Origin.Stream.Rotation), (p.Width < p.Height) != quarterTurn(p.Rotation)
	return judge.FailIf(source != landed, "the picture lands "+orientation(landed)+", the source displays "+orientation(source))
}

func quarterTurn(rotation int) bool { return rotation%180 != 0 }

func orientation(portrait bool) string {
	if portrait {
		return "portrait"
	}
	return "landscape"
}
