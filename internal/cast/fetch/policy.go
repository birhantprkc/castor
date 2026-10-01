// Package fetch decides how a cast fetches the bytes of a source, and how fast.
package fetch

import (
	"cmp"
	"time"

	"github.com/stupside/castor/internal/cast/container"
)

const BackoffMax = 60 * time.Second

const EncodeBurstSeconds = 10

type Pace struct {
	// Realtime is the media seconds per wall-clock second the read is allowed.
	Realtime float64

	// Burst is how much of the stream may be read at wire speed before Realtime binds.
	Burst time.Duration

	// Catchup is the rate a read that fell behind its schedule takes until it is back on it; 0 is Realtime.
	Catchup float64
}

func (p Pace) capped(ceiling Pace) Pace {
	if ceiling.Realtime <= 0 {
		return p
	}
	if p.Realtime <= 0 {
		return ceiling
	}
	capped := Pace{Realtime: min(p.Realtime, ceiling.Realtime), Burst: min(p.Burst, ceiling.Burst)}
	if catchup := min(cmp.Or(p.Catchup, p.Realtime), cmp.Or(ceiling.Catchup, ceiling.Realtime)); catchup > capped.Realtime {
		capped.Catchup = catchup
	}
	return capped
}

var (
	paceVOD = Pace{Realtime: 2.0, Burst: 90 * time.Second}
	// A live read held to 1x never wins back what a stalled segment cost it, and falls off the window.
	paceLive = Pace{Realtime: 1.0, Catchup: paceVOD.Realtime}
	// pacePlayback asks for a link at the rate it is played, with no burst.
	pacePlayback = Pace{Realtime: 1.0}
)

// paceBurning keeps the subtitle-burning encoder just above realtime.
var paceBurning = Pace{Realtime: 1.15, Burst: EncodeBurstSeconds * time.Second}

// paceSegmentWindow is a client draining a segment window at 1x.
var paceSegmentWindow = Pace{Realtime: 1.0, Burst: container.HLSWindow}

// Ceiling is the pace an encode's output imposes over whatever its input was read at.
func Ceiling(segmentedOutput, burning bool) Pace {
	switch {
	case burning:
		return paceBurning
	case segmentedOutput:
		return paceSegmentWindow
	default:
		return Pace{}
	}
}

const segmentOpenRetries = 3

type Policy struct {
	// Name and Why identify the row this came from.
	Name string
	Why  string

	// Deadline is how long ONE read may stall before ffmpeg abandons it and reconnects (-rw_timeout).
	Deadline time.Duration

	SegmentRetries int

	// Pace is how fast the source may be consumed.
	Pace Pace
}
