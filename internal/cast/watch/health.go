package watch

import (
	"fmt"
	"time"

	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// Window is which side of the playback gate a judgement is being made on. It is the
// one axis that changes what a verdict may DO rather than what it is, which is why it
// is a field on a rule and a key of the action table instead of a second ruleset.
type Window int

const (
	// BeforePlay is the pre-playback window: the source is being read into a buffer and
	// no renderer holds a URL yet, so every fault here is still revisable.
	BeforePlay Window = iota
	// Opening is a delivery waiting for the artifact a renderer will be pointed at: the
	// first byte of a stream, or the playlist of a segmented directory. Still before
	// Play, and judged on the artifact rather than on the read behind it.
	Opening
	// Playing is the in-flight window: the renderer holds a URL and is fetching. This
	// window existed nowhere before, which is why a cast that starved after playback
	// began ran to the end of the title and reported success.
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
	// Starting is the zero value because it is the honest answer before anything has
	// been established: nothing is wrong and nothing is proven.
	Starting Kind = iota
	// Ready means the wait may end: the encoder can start reading, or the renderer can
	// be pointed at the artifact.
	Ready
	// Healthy is a cast in flight with nothing against it.
	Healthy
	// Stalled is a producer that is still supposed to be delivering and has delivered
	// nothing for the derived stall window.
	Stalled
	// Undeliverable is media arriving slower than it will be played, measured as
	// ffmpeg's own speed against the pace the read was allowed.
	Undeliverable
	// Dead is a producer that reached a terminal state with nothing playable behind it.
	Dead
	// Unfetched is a renderer that accepted the URL and has never come for the bytes.
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

// Health is everything a rule judges about one cast, as numbers. It is a value rather
// than a stretch of loop body because the branches it decides (a transcription lead, an
// upstream that stopped delivering, a read that cannot keep up with playback, a
// renderer that never came for the bytes) are the rules of this layer, and inline they
// were rules no test in the repository could reach.
//
// Every field is a measurement or a terminal state, never a decision. A zero Health is
// a cast about which nothing has been established, and it must resolve to Starting in
// every window rather than to a fault.
type Health struct {
	// Landed is how many bytes of the artifact a renderer would fetch exist now: the
	// spool the encoder tails, the stream the replay delivery has spooled, the playlist the
	// muxer has written. One field, because "is there anything to hand over" is one
	// question whatever the delivery.
	Landed int64

	// SinceGrowth is how long Landed has held the same value.
	SinceGrowth time.Duration

	// Position is how much media the producer says it has delivered, and Speed how many
	// media seconds per wall-clock second that worked out to. Speed is ffmpeg's own
	// cumulative figure, so it answers "has this read, over its whole life, kept up with
	// playback", which is exactly the question a renderer's buffer asks.
	Position time.Duration
	Speed    media.Speed

	// Samples is how many times the producer has stated a speed at all. It is the
	// confidence behind Speed and nothing else: ffmpeg answers N/A for every progress
	// field until the first packet is muxed, so an early sample is not a slow read.
	Samples int

	// Headroom is the pace the SOURCE read was allowed, as a multiple of realtime (see
	// read.Pace.Realtime). Zero withholds the deliverability question entirely, and it is
	// the answer wherever the LINK is not what decides how fast the subject runs: the
	// subtitle-burning encoder is deliberately pinned just above realtime, and a source
	// read castor itself throttles (a PCM tee whose consumer runs whisper inference on the
	// goroutine draining it) or produces (a floor encode) is measured on its own work.
	//
	// Read as a starving link, each of those costs a cast that would have played: before
	// playback it is refused and every other admitted link is burned looking for a better
	// one, and in flight it is abandoned mid-title with a user sent after their network. So
	// the term is one answer about the read rather than one per window, and the supplier
	// states it once for both (see pipeline's pull.judgedPace).
	Headroom float64

	// Ended reports that the producer has reached its terminal state, and Failed that it
	// got there with an error. A finished producer is not a stalled one: a completed
	// read's buffer never grows again, and reporting that as a stall would fail every
	// short source.
	Ended  bool
	Failed bool

	// Overdue reports that the artifact has taken longer to appear than this delivery is
	// willing to wait for it. It is a fact about the delivery's own patience, not about
	// the producer: a slow upstream can legitimately take that long to yield a first
	// byte, and the delivery is better off proceeding and letting the renderer's own
	// buffering wait.
	Overdue bool

	// Subtitles reports that this cast burns in a transcription, which is what makes
	// Lead and LeadDone describe a live stage rather than nothing. Lead is the
	// transcription's committed frontier in seconds of media, and LeadDone reports that
	// it has finished, which short sources reach before they ever build a lead.
	Subtitles bool
	Lead      float64
	LeadDone  bool

	// Handed is how many bytes of what this delivery made the renderer has actually been
	// given, and SinceFetch how long ago a byte last moved (measured from the start of the
	// watch while none has).
	//
	// What it took and not how often it asked: a delivery counts a request before it writes a byte
	// of the response, and a renderer's first move on a stream URL is a probe (see
	// watch.Consumer), so a count reads as a fetch for a renderer that came to the door and
	// took nothing. That is the whole of the shape the one renderer verdict is for.
	Handed     int64
	SinceFetch time.Duration

	// Delivered is how much media the renderer can already fetch: the position the
	// encoder feeding the delivery reports, which on a delivery that never takes back what
	// it produced is the whole of the program a viewer still has in hand. SincePlay is how
	// long the renderer has held the URL, which bounds how much of that it can have played,
	// because playback runs at exactly playbackRate.
	//
	// The pair is what separates a producer that stopped from a cast that is over, and that
	// is the only question it answers. A read allowed twice realtime is half an hour ahead of
	// the viewer at the one-hour mark, so an upstream that goes quiet there costs the tail of
	// the title and nothing else; ending the cast on the spot takes the thirty minutes already
	// on disk away from someone who was watching them. It says nothing about the RENDERER,
	// because it is filled by the encoder rather than by anybody fetching: it keeps growing
	// through a renderer's silence, which is why no rule here convicts one on it.
	//
	// Both are zero wherever no renderer holds a URL, which is what leaves the pre-playback
	// stall (a playlist whose segments all answer 403) judged on silence alone.
	Delivered time.Duration
	SincePlay time.Duration

	// SinceDeficit is how long the read has continuously been delivering less than
	// playback consumes. It is what separates a link that is losing the race from one
	// that dipped: a rate-limited CDN goes quiet for a full backoff ceiling and then
	// lands the retry, and convicting inside that window blames castor's own impatience.
	SinceDeficit time.Duration
}

// String names the measurements for a log line and for the fault a verdict carries,
// where they are the whole of what anyone has to act on.
func (h Health) String() string {
	return fmt.Sprintf("landed=%d position=%s speed=%.4gx headroom=%.4gx samples=%d since_growth=%s handed=%d since_fetch=%s buffered=%s",
		h.Landed, h.Position.Round(time.Second), float64(h.Speed), h.Headroom,
		h.Samples, h.SinceGrowth.Round(time.Second), h.Handed, h.SinceFetch.Round(time.Second),
		h.buffer().Round(time.Second))
}

// playable reports that there is something to hand a renderer.
func (h Health) playable() bool { return h.Landed > 0 }

// buffer is how much media castor can prove the renderer has left to play: everything the
// delivery has made fetchable, less the most a viewer can have consumed of it. Negative
// once the renderer has caught up with what was produced for it.
//
// The subtraction is the whole measurement, and it is a LOWER bound in the one direction
// that matters. A renderer cannot play media the encoder has not produced, and it cannot
// play faster than playbackRate, so anything positive here is media in hand; the terms it
// leaves out (the seconds a renderer spends buffering before it starts, and a viewer's
// pauses) all make the real figure larger. Nothing may abandon a cast on the strength of
// this being small, only on it being gone.
func (h Health) buffer() time.Duration { return h.Delivered - h.SincePlay }

// buffered reports that the renderer still has media to play whatever happens next
// upstream, which is what makes a producer that stopped something other than a cast to end.
func (h Health) buffered() bool { return h.buffer() > 0 }

// leads reports that the transcription is far enough ahead of the encoder, or has
// finished, which short sources reach before they ever build the lead.
func (h Health) leads() bool { return h.Lead >= transcriptionLeadSeconds || h.LeadDone }

// measured reports that the deliverability question has had its chance to be answered,
// which is what the pre-playback gate holds for.
//
// The three ways past it are all the absence of a question rather than an answer to it.
// A read that has ENDED delivered everything it was going to; its average rate is a
// fact about the past and not a prediction. A read with no headroom above realtime
// (a live edge, a local encode, or a read whose rate castor itself is deciding) can never
// produce a deficit that means anything, because it was never allowed to run ahead, or
// never allowed to by the link. Otherwise the reader must have stated a speed the derived
// number of times.
//
// The hold this imposes is bounded by the reader and not by the source: a byte in the
// artifact means the muxer wrote a packet, and a muxed packet means the next progress
// block carries a real speed, so the wait is minSpeedSamples report periods and not a
// hostage to how slow the origin is.
func (h Health) measured() bool {
	return h.Ended || h.Headroom <= 1 || h.Samples >= minSpeedSamples
}

// starving reports a read delivering less media per wall-clock second than playback
// will consume, on enough evidence to say so.
//
// Every clause is load-bearing, and the calibration is wide. A healthy cast of a
// 1920x800 h264 source measured 2.100 against a readrate of 2.0, i.e. the reader was
// AHEAD of the pace it was allowed and throttling itself; every cast that died measured
// 0.0627, 0.109, 0.159 or 0.39 against the same 2.0. The floor is playback itself
// rather than a fraction of the headroom, because below realtime the renderer's buffer
// must eventually drain whatever the link was allowed, and above it the cast can be
// watched however far short of its allowance the reader falls.
//
// Headroom above realtime is what makes the comparison mean anything: a live edge is
// paced at exactly 1.0 and cannot be outrun, so a live source delivering 0.98 has
// answered nothing about the link, while a VOD source allowed 2.0 with a wire-speed
// burst that still cannot reach realtime has answered everything. A whisper cast reads
// PCM off the same 2x read and reports the same 2x speed, so it is judged by the same
// clause and passes it.
//
// A read that has ended is never starving: it delivered the whole program, and a short
// source read in less than the confidence window would otherwise be convicted by the
// arithmetic of its own startup cost.
func (h Health) starving() bool {
	return !h.Ended && h.Headroom > 1 && h.Samples >= minSpeedSamples && h.Speed < playbackRate
}

const (
	// transcriptionLeadSeconds is how far ahead of the encoder whisper must be before
	// playback starts. The encoder is pinned to realtime after an initial burst while
	// the pull feeds whisper at up to 2x, so once this gate opens the lead only grows.
	// The margin past the burst covers the streaming policy's commit lag:
	// LocalAgreement holds words back until a second hypothesis confirms them.
	transcriptionLeadSeconds = read.EncodeBurstSeconds + 10

	// StallWindow is how long a producer that is still supposed to be delivering may
	// deliver nothing before castor stops waiting on it. It catches the CDN that keeps the
	// connection open but sends nothing (throttled or burned token), which would
	// otherwise hang a cast in silence forever, and the renderer that stops fetching
	// what castor is still producing for it.
	//
	// On most reads ffmpeg's own -rw_timeout/-reconnect answer a transient drop first and
	// this is the backstop. On one read it is the ONLY answer: a source whose fragments must
	// arrive whole is read with no mid-read deadline at all, because abandoning a fragment
	// partway through corrupts it fatally (see read's segment-fragile row), so nothing but
	// this window ever notices that such a read has gone quiet.
	//
	// It is derived from the reconnect ceiling rather than picked, because a judgement
	// that fires inside the backoff it handed the reader is not observing a stall, it is
	// causing one: a rate-limited CDN answers 429 and ffmpeg waits out the full ceiling
	// before the retry that finally lands, so at exactly the instant a legitimate
	// backoff was about to succeed the old sixty seconds killed the cast and blamed an
	// expired playlist. Two ceilings plus a margin is a bound on the slowest recovery
	// that can still deliver: one full wait that fails, a second that succeeds, and time
	// for its bytes to arrive. Past that, nothing is coming.
	//
	// It is derived from the read policy's ceiling and not from the flag renderer's: the
	// bound belongs to whatever decided how long a retry may be waited out, and asking an
	// argument builder is how a judgement about health came to depend on a command line.
	//
	// It is exported because anything else that gives up on a silent peer has to outlast
	// it, or it severs a connection this layer has not yet finished judging.
	StallWindow = 2*read.BackoffMax + 30*time.Second

	// deficitWindow is how long a read may deliver less than playback consumes before the
	// pre-playback arm convicts it. One reconnect ceiling, and derived from the same
	// number as StallWindow for the same reason: ffmpeg is handed that ceiling to wait
	// out a 429, so during it the read legitimately delivers nothing while its cumulative
	// speed sinks, and a judgement reached inside the window it granted the reader is
	// reporting castor's own impatience as a starving link.
	//
	// One ceiling and not the two the in-flight arm waits, because the stakes are not the
	// same: before playback the answer is to revise the attempt (a rendition the link can
	// carry), while in flight it ends a cast someone is watching. Where the deficit really
	// is continuous, this bounds how long the gate holds before castor acts.
	deficitWindow = read.BackoffMax

	// fetchWindow is how long a renderer that accepted the URL may take to come for the
	// bytes. It is one reconnect ceiling: a renderer that answered Play is on the local
	// network holding a local URL, so it has strictly less to do than an origin waiting
	// out a rate limit before the retry that lands, and one such ceiling is a generous
	// bound on a request it has nothing to wait for. Past it the renderer is not slow,
	// it is not coming, and the observed run's bytes_sent=0 is what that looks like.
	fetchWindow = read.BackoffMax

	// pollInterval is how often the facts are re-read. It is well under the producer's
	// report period so no sample is missed, and well under every window derived above so
	// none of them is overshot by the cadence that observes it.
	pollInterval = 200 * time.Millisecond

	// reportInterval is how often a watch with nothing to decide says what it is waiting
	// on, so a slow start is never a silent one.
	reportInterval = 5 * time.Second
)

// playbackRate is the pace a renderer consumes media at, and therefore the floor a
// read has to clear to be watchable at all. It is 1.0 by definition and not by choice:
// a second of picture takes a second to play.
const playbackRate media.Speed = 1

// startupLag is what a read spends before it can state a speed at all: DNS, the TLS
// handshake, the first HTTP round trip and the demuxer's own probe. It is a measurement
// and not an allowance, taken from a healthy cast that logged its first packet "after a
// lag of 2.473s", and it matters because ffmpeg's speed is cumulative from process start:
// every block a read states carries that cost in its denominator, so the first ones are
// arbitrarily bad through no fault of the link.
const startupLag = 2473 * time.Millisecond

// minSpeedSamples is how many times the reader must have stated a speed before that
// speed is allowed to convict it, and the confidence window the pre-playback hold exists
// to fill. Decision 3 names it as the one dial: shrink this if the added
// time-to-first-frame ever proves unacceptable, rather than removing the hold.
//
// It is a count of reader report blocks (read.StatsPeriod each), and the property that
// fixes it is that the stated window must OUTLAST the startup lag above, which is what
// TestTheConfidenceWindowOutlastsTheStartupLagItCarries pins. A window shorter than the
// lag is arithmetic about the handshake: at four blocks a link genuinely delivering 2x is
// still cumulatively under realtime, so the number that convicts is the connection's cost
// and not the link's rate. Six blocks is three seconds of stated speed, past the lag with
// a block to spare, by which point a healthy read is deep inside its wire-speed burst and
// reporting multiples of realtime (a real one measured 2.100x) while a starving one has
// only sunk further: the observed failure went 0.159 to 0.0627 over ten seconds.
const minSpeedSamples = 6
