package health

import (
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Producer is what makes the watched bytes: when it ended, and what it printed.
type Producer interface {
	Done() <-chan struct{}
	Evidence() []string
}

// Telemetry is a producer's media pace, judged in media rather than bytes a muxer pads.
type Telemetry interface {
	Progress() media.Progress
	Err() error
}

// Lead is how far a transcription has committed, in seconds of media, and whether it has finished.
type Lead interface {
	LatestEnd() float64
	Done() bool
}

// Audience is what the device took, to tell a pause from a device gone.
type Audience interface {
	// Handed is the bytes taken and when the last one moved.
	Handed() (bytes int64, last time.Time)

	// Buffered is the media the device can fetch; a rolling window caps it at its length.
	Buffered() time.Duration
}
