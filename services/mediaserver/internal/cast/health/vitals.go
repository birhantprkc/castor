package health

import (
	"fmt"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/fetch"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Vitals is one reading of a watched cast: its measurements and terminal states.
type Vitals struct {
	// Landed is the bytes of artifact available now.
	Landed int64

	// sinceGrowth is how long no new media has arrived: Position when the producer states one, else Landed.
	sinceGrowth time.Duration

	// Position is the media made so far, and Speed that media per wall-clock second.
	Position time.Duration
	Speed    media.Speed

	// Samples counts the speeds the producer stated, none before its first packet.
	Samples int

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
}

func (h Vitals) String() string {
	return fmt.Sprintf("landed=%d position=%s speed=%.4gx samples=%d since_growth=%s handed=%d since_fetch=%s buffered=%s",
		h.Landed, h.Position.Round(time.Second), float64(h.Speed),
		h.Samples, h.sinceGrowth.Round(time.Second), h.handed, h.sinceFetch.Round(time.Second),
		h.buffer().Round(time.Second))
}

// playable reports that there is something to hand a device.
func (h Vitals) playable() bool { return h.Landed > 0 }

// buffer is the least media the device provably still has to play.
func (h Vitals) buffer() time.Duration { return h.delivered - h.sincePlay }

// buffered tells a device still playing from one that ran out.
func (h Vitals) buffered() bool { return h.buffer() > 0 }

// leads is a transcription far enough ahead, or finished.
func (h Vitals) leads() bool { return h.lead >= transcriptionLead.Seconds() || h.leadDone }

// cushioned is a buffer deep enough to ride out a source that goes quiet for a moment.
func (h Vitals) cushioned() bool { return h.Position >= readCushion }

const (
	// readCushion is the media a read holds before a device is started, since a device gives up on a stall long before one is judged.
	readCushion = 30 * time.Second

	// transcriptionLead is how far a burn-in leads before playback starts.
	transcriptionLead = fetch.EncodeBurst + 10*time.Second

	// StallWindow outlasts two reconnect ceilings, so a reconnecting read is never called silent.
	StallWindow = 2*fetch.BackoffMax + 30*time.Second

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
