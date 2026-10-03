// Package fetch decides how a cast fetches the bytes of a source, and how fast.
package fetch

import (
	"cmp"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/container"
)

// BackoffMax is the longest ffmpeg waits before it reconnects to a source.
const BackoffMax = 60 * time.Second

// EncodeBurst is how much media a subtitle-burning encode reads ahead at wire speed.
const EncodeBurst = 10 * time.Second

// Pace is how fast a read may consume its source.
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
var paceBurning = Pace{Realtime: 1.15, Burst: EncodeBurst}

// paceSegmentWindow is a client draining a segment window at 1x.
var paceSegmentWindow = Pace{Realtime: 1.0, Burst: container.HLSWindow}

// Ceiling is the pace an encode's output imposes over whatever its input was read at.
func Ceiling(output container.DeliveryKind, burning bool) Pace {
	switch {
	case burning:
		return paceBurning
	case output == container.DeliverSegmented:
		return paceSegmentWindow
	default:
		return Pace{}
	}
}

// segmentOpenRetries is how many times a segment that would not open is asked for again.
const segmentOpenRetries = 3

// Policy is how one input is fetched: how long a read may stall, how often a segment is retried, and how fast.
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
