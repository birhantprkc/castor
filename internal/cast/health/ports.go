package health

import (
	"time"

	"github.com/stupside/castor/internal/media"
)

// Producer is an observation surface reporting state and evidence; it never decides.
type Producer interface {
	Done() <-chan struct{}
	Evidence() []string
}

// Telemetry reports pace (only sources); media speed fixes starved-upstream bug.
type Telemetry interface {
	Progress() media.Progress
	Err() error
}

// Lead is how far a transcription has committed, in seconds of media, and whether it has finished.
type Lead interface {
	LatestEnd() float64
	Done() bool
}

// Audience is the viewer's requests and media, one port, to tell a pause from a renderer gone.
type Audience interface {
	// Handed: bytes handed and last move time; counts bytes not requests.
	Handed() (bytes int64, last time.Time)

	// Buffered: media renderer can fetch (rolling windows must cap at their window).
	Buffered() time.Duration
}
