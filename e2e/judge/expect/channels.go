package expect

import (
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/judge"
	"github.com/stupside/castor/e2e/strategy"
)

// Channels builds a check that audio lands with exactly n channels, as in `channels: 6`.
type Channels struct{}

func (Channels) Name() string { return "channels" }

func (Channels) Build(settings yaml.Node) (judge.Check, error) {
	var n int
	if err := strategy.Decode(settings, &n); err != nil || n < 1 {
		return nil, fmt.Errorf("channels: want the channel count audio must land with, at least 1 (%v)", err)
	}
	return channels(n), nil
}

type channels int

func (channels) Name() string { return "channels" }

func (n channels) Judge(e judge.Evidence) []string {
	p := e.Received.Played
	if p.AudioPackets == 0 {
		return []string{fmt.Sprintf("no audio landed, want %d channels", n)}
	}
	return judge.FailIf(p.Channels != int(n), fmt.Sprintf("audio landed as %d channels, want %d", p.Channels, n))
}
