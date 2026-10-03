package receiver

import (
	"testing"
	"time"

	"github.com/stupside/castor/e2e/strategy"
)

// Family builds a Device from a case's settings; its name is the device.type castor pins the receiver with.
type Family = strategy.Factory[Device]

// Device is a receiver as a case configures it: it serves its protocol and turns its hand-off into a Session's.
type Device interface {
	// Type is the device.type castor pins it with.
	Type() string
	Start(t *testing.T, s *Session) (Endpoint, error)
}

// Endpoint is where castor reaches a started receiver, and what that receiver decodes.
type Endpoint struct {
	Host  string
	Plays Plays
	// EndsWithin is how long castor may take to notice playback ended, as far as this protocol lets it.
	EndsWithin time.Duration
}

// Reported is EndsWithin for a protocol that tells castor about the end: polled every 2s, so anything longer is a hang.
const Reported = 15 * time.Second

// Plays is what a receiver decodes, codecs named as ffprobe names them.
type Plays struct {
	// Video maps each codec to the deepest bit depth it decodes.
	Video map[string]int `yaml:"video"`
	// Audio maps each codec to its channel ceiling, 0 for none.
	Audio map[string]int `yaml:"audio"`
	// Levels maps a codec to the highest level it decodes, in ffprobe's units (h264 x10); absent means no ceiling.
	Levels map[string]int `yaml:"levels"`
	// MaxSampleRate is the highest audio sample rate the receiver plays, 0 for no ceiling.
	MaxSampleRate int `yaml:"max_sample_rate"`
}
