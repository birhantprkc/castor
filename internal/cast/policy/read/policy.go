// Package read holds the fetch and pace decisions for the bytes of a source.
package read

import (
	"time"

	"github.com/stupside/castor/internal/container"
	"github.com/stupside/castor/internal/media"
)

const BackoffMax = 60 * time.Second

const EncodeBurstSeconds = 10

type Pace struct {
	// Realtime is the media seconds per wall-clock second the read is allowed.
	Realtime float64

	// Burst is how much of the stream may be read at wire speed before Realtime binds.
	Burst time.Duration
}

func (p Pace) capped(ceiling Pace) Pace {
	if ceiling.Realtime <= 0 {
		return p
	}
	if p.Realtime <= 0 {
		return ceiling
	}
	return Pace{Realtime: min(p.Realtime, ceiling.Realtime), Burst: min(p.Burst, ceiling.Burst)}
}

var (
	paceVOD  = Pace{Realtime: 2.0, Burst: 90 * time.Second}
	paceLive = Pace{Realtime: 1.0}
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

// Encoding is the plan an encode reads a source with: every input under the ceiling, or with none, only segmented inputs paced.
func (p Plan) Encoding(program media.Program, ceiling Pace) Plan {
	plan := p.Clone()
	for _, input := range program.Inputs {
		policy, ok := plan[input.ID]
		if !ok {
			continue
		}
		// Unbounded, only a playlist is paced, so a CDN does not answer a burst with 429s.
		if ceiling.Realtime <= 0 && !input.Fetching().Segmented {
			policy.Pace = Pace{}
		}
		policy.Pace = policy.Pace.capped(ceiling)
		plan[input.ID] = policy
	}
	return plan
}

var transient = []int{429, 500, 502, 503, 504}

const segmentOpenRetries = 3

type Policy struct {
	// Name and Why identify the row this came from.
	Name string
	Why  string

	// Deadline is how long ONE read may stall before ffmpeg abandons it and reconnects (-rw_timeout).
	Deadline time.Duration

	SegmentRetries int

	Backoff time.Duration

	RetryStatuses []int

	// Pace is how fast the source may be consumed.
	Pace Pace
}
