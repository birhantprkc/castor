package invariant

import (
	"fmt"
	"slices"

	"github.com/stupside/castor/e2e/judge"
)

// SoundFollowsSource holds that audio landed exactly when the source had it, in a codec, channel count and sample rate the receiver takes.
type SoundFollowsSource struct{}

func (SoundFollowsSource) Name() string { return "sound" }

func (SoundFollowsSource) Judge(e judge.Evidence) []string {
	p, published := e.Received.Played, e.Origin.Stream.Channels
	if p.AudioPackets == 0 {
		return judge.FailIf(published > 0, fmt.Sprintf("no audio landed, but the source published %d channels", published))
	}
	plays := e.Endpoint.Plays
	ceiling, decodes := plays.Audio[p.Audio]
	return slices.Concat(
		judge.FailIf(published == 0, "audio landed from a silent source"),
		judge.FailIf(!decodes, fmt.Sprintf("audio %q is none the receiver decodes", p.Audio)),
		judge.FailIf(decodes && ceiling > 0 && p.Channels > ceiling, fmt.Sprintf("audio has %d channels, above the receiver's %d", p.Channels, ceiling)),
		judge.FailIf(plays.MaxSampleRate > 0 && p.SampleRate > plays.MaxSampleRate, fmt.Sprintf("audio lands at %d Hz, above the receiver's %d", p.SampleRate, plays.MaxSampleRate)),
	)
}
