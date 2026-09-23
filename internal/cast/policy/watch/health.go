package watch

import (
	"fmt"
	"time"

	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/media"
)

// Window: which side of playback gate verdict is made.
type Window int

const (
	// Source buffering; no renderer URL yet; faults revisable.
	BeforePlay Window = iota
	// Delivery waiting; judged on artifact, not read.
	Opening
	// Renderer holds URL and is fetching.
	Playing
)

func (w Window) String() string {
	switch w {
	case Opening:
		return "opening"
	case Playing:
		return "playing"
	default:
		return "before-play"
	}
}

// Kind is the verdict a rule reaches about a cast.
type Kind int

const (
	// Zero value: nothing wrong, nothing proven.
	Starting Kind = iota
	// Encoder ready to read or renderer ready for artifact.
	Ready
	// Cast in flight with nothing against it.
	Healthy
	// Producer silent for stall window.
	Stalled
	// Media slower than playback pace.
	Undeliverable
	// Producer terminal state with nothing playable.
	Dead
	// Renderer accepted URL but never fetched.
	Unfetched
)

func (k Kind) String() string {
	switch k {
	case Ready:
		return "ready"
	case Healthy:
		return "healthy"
	case Stalled:
		return "stalled"
	case Undeliverable:
		return "undeliverable"
	case Dead:
		return "dead"
	case Unfetched:
		return "unfetched"
	default:
		return "starting"
	}
}

// Health: measurements and terminal states; zero resolves to Starting.
type Health struct {
	// Bytes of artifact available now.
	Landed int64

	// SinceGrowth is how long Landed has held the same value.
	SinceGrowth time.Duration

	// Media delivered; Speed: cumulative media per clock second.
	Position time.Duration
	Speed    media.Speed

	// Count of speed reports; N/A until first packet muxed.
	Samples int

	// Pace source read was allowed, as realtime multiple; zero withholds deliverability.
	Headroom float64

	// Terminal state; finished != stalled (buffer doesn't grow again).
	Ended  bool
	Failed bool

	// Overdue is a fact about the delivery's own patience, not about the producer.
	Overdue bool

	// Subtitle transcription; Lead: committed frontier; LeadDone: finished.
	Subtitles bool
	Lead      float64
	LeadDone  bool

	// Bytes renderer received; when byte last moved; counts bytes not requests.
	Handed     int64
	SinceFetch time.Duration

	// Media renderer can fetch; URL hold; separates producer stop from cast end.
	Delivered time.Duration
	SincePlay time.Duration

	// Separates link losing race from temporary dip.
	SinceDeficit time.Duration
}

func (h Health) String() string {
	return fmt.Sprintf("landed=%d position=%s speed=%.4gx headroom=%.4gx samples=%d since_growth=%s handed=%d since_fetch=%s buffered=%s",
		h.Landed, h.Position.Round(time.Second), float64(h.Speed), h.Headroom,
		h.Samples, h.SinceGrowth.Round(time.Second), h.Handed, h.SinceFetch.Round(time.Second),
		h.buffer().Round(time.Second))
}

// playable reports that there is something to hand a renderer.
func (h Health) playable() bool { return h.Landed > 0 }

// Provable media renderer can play; lower bound.
func (h Health) buffer() time.Duration { return h.Delivered - h.SincePlay }

// Renderer has media to play; distinguishes stop from end.
func (h Health) buffered() bool { return h.buffer() > 0 }

// Transcription ahead or finished.
func (h Health) leads() bool { return h.Lead >= transcriptionLeadSeconds || h.LeadDone }

// Deliverability resolved: read ended, no headroom, or enough samples.
func (h Health) measured() bool {
	return h.Ended || h.Headroom <= 1 || h.Samples >= minSpeedSamples
}

// Read slower than playback; needs headroom and enough samples.
func (h Health) starving() bool {
	return !h.Ended && h.Headroom > 1 && h.Samples >= minSpeedSamples && h.Speed < playbackRate
}

const (
	// Transcription lead before playback starts.
	transcriptionLeadSeconds = read.EncodeBurstSeconds + 10

	// Silence timeout; derived from read policy's reconnect ceiling.
	StallWindow = 2*read.BackoffMax + 30*time.Second

	// One reconnect ceiling; legitimate backoff timeout.
	deficitWindow = read.BackoffMax

	// Reconnect ceiling; renderer on local network, so shorter window.
	fetchWindow = read.BackoffMax

	// Sample cadence; under producer's report period.
	pollInterval = 200 * time.Millisecond

	// How often watch reports on nothing-to-decide state.
	reportInterval = 5 * time.Second
)

// Renderer's media consumption pace; 1.0 by definition.
const playbackRate media.Speed = 1

// Sample count for conviction; must outlast startup lag.
const minSpeedSamples = 6
