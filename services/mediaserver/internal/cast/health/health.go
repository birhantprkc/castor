package health

import (
	"fmt"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/fetch"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Phase is how far a cast's attempt has got; its order is the safety property, since nothing in castor seeks.
type Phase int

const (
	// Unstarted is an attempt that ended before anything was read.
	Unstarted Phase = iota
	// Reading is the source read landing media with no device pointed at anything yet; faults are revisable.
	Reading
	// Opening is the artifact a device will be pointed at being produced, judged on it rather than the read.
	Opening
	// Playing is the device holding the URL: the line a recovery does not cross.
	Playing
	// Delivered is the cast having run its course.
	Delivered
)

func (p Phase) String() string {
	switch p {
	case Reading:
		return "reading"
	case Opening:
		return "opening"
	case Playing:
		return "playing"
	case Delivered:
		return "delivered"
	default:
		return "unstarted"
	}
}

// Kind is the verdict a rule reaches about a cast.
type Kind int

const (
	// starting is nothing wrong and nothing proven.
	starting Kind = iota
	// ready is a buffer an encoder may read, or an artifact a device may fetch.
	ready
	// healthy is a cast in flight with nothing against it.
	healthy
	// Stalled is a producer silent for the stall window.
	Stalled
	// Undeliverable is media arriving slower than playback.
	Undeliverable
	// Dead is a producer that ended with nothing playable.
	Dead
	// Unfetched is a device that accepted the URL and never fetched it.
	Unfetched
)

func (k Kind) String() string {
	switch k {
	case ready:
		return "ready"
	case healthy:
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

// Health is one reading of a watched cast: its measurements and terminal states.
type Health struct {
	// Landed is the bytes of artifact available now.
	Landed int64

	// sinceGrowth is how long no new media has arrived: Position when the producer states one, else Landed.
	sinceGrowth time.Duration

	// Position is the media made so far, and Speed that media per wall-clock second.
	Position time.Duration
	Speed    media.Speed

	// Samples counts the speeds the producer stated, none before its first packet.
	Samples int

	// Headroom is the pace the read was allowed, as a multiple of realtime; zero judges no deliverability.
	Headroom float64

	// ended is a producer that finished, which never stalls since its buffer will not grow again.
	ended  bool
	failed bool

	// overdue is a fact about the delivery's own patience, not about the producer.
	overdue bool

	// lead is how far a burn-in's transcription has committed, and leadDone whether it finished.
	subtitles bool
	lead      float64
	leadDone  bool

	// handed is the bytes the device took, and sinceFetch how long since one moved.
	handed     int64
	sinceFetch time.Duration

	// delivered is the media the device can fetch, and sincePlay how long it has held the URL.
	delivered time.Duration
	sincePlay time.Duration

	// sinceDeficit tells a link losing the race from a passing dip.
	sinceDeficit time.Duration
}

func (h Health) String() string {
	return fmt.Sprintf("landed=%d position=%s speed=%.4gx headroom=%.4gx samples=%d since_growth=%s handed=%d since_fetch=%s buffered=%s",
		h.Landed, h.Position.Round(time.Second), float64(h.Speed), h.Headroom,
		h.Samples, h.sinceGrowth.Round(time.Second), h.handed, h.sinceFetch.Round(time.Second),
		h.buffer().Round(time.Second))
}

// playable reports that there is something to hand a device.
func (h Health) playable() bool { return h.Landed > 0 }

// buffer is the least media the device provably still has to play.
func (h Health) buffer() time.Duration { return h.delivered - h.sincePlay }

// buffered tells a device still playing from one that ran out.
func (h Health) buffered() bool { return h.buffer() > 0 }

// leads is a transcription far enough ahead, or finished.
func (h Health) leads() bool { return h.lead >= transcriptionLeadSeconds || h.leadDone }

// measured is deliverability settled: the read ended, was given no headroom, or stated enough speeds.
func (h Health) measured() bool {
	return h.ended || h.Headroom <= 1 || h.Samples >= minSpeedSamples
}

// starving is a read with headroom, measured slower than playback.
func (h Health) starving() bool {
	return !h.ended && h.Headroom > 1 && h.Samples >= minSpeedSamples && h.Speed < playbackRate
}

const (
	// transcriptionLeadSeconds is how far a burn-in leads before playback starts.
	transcriptionLeadSeconds = fetch.EncodeBurstSeconds + 10

	// StallWindow outlasts two reconnect ceilings, so a reconnecting read is never called silent.
	StallWindow = 2*fetch.BackoffMax + 30*time.Second

	// deficitWindow is one reconnect ceiling, the longest a legitimate backoff dips.
	deficitWindow = fetch.BackoffMax

	// fetchWindow is one reconnect ceiling: a device on the local network needs no longer.
	fetchWindow = fetch.BackoffMax

	// paceSpan is how far back growth is judged, and minPace the share of playback that counts as growing.
	paceSpan = 10 * time.Second
	minPace  = 0.5

	// pollInterval is under the producer's report period, so no sample is missed.
	pollInterval = 200 * time.Millisecond

	// reportInterval is how often a watch with nothing to decide says so.
	reportInterval = 5 * time.Second
)

// playbackRate is the pace a device plays at.
const playbackRate media.Speed = 1

// minSpeedSamples outlasts a producer's startup lag before its speed convicts it.
const minSpeedSamples = 6
