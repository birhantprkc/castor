package read

import (
	"fmt"
	"time"

	"github.com/stupside/castor/internal/media"
)

// BackoffMax is the ceiling every upstream fetch castor makes is given: how long
// ffmpeg may keep retrying one read before it gives up and the fetch fails.
// Exported because a caller that waits on an upstream has to wait longer than this
// to be waiting on the upstream at all. A deadline shorter than the ceiling can only
// ever fire mid-backoff, on a retry that was still owed its chance to succeed, and it
// then reports the retry as the failure.
//
// It lives here rather than beside the flag it renders into, because the judgement
// that derives from it (the playback gate's stall bound) has to be readable without
// consulting an argument builder, and because a rule about how long to keep trying
// is a read policy whichever process ends up spelling it.
const BackoffMax = 60 * time.Second

// EncodeBurstSeconds is how much of the stream the subtitle-burning encoder may race
// through at full speed before its pace pins it to realtime. Exported because the
// playback gate's transcription lead must cover it: frames encoded during the burst
// need their cues committed before the encode starts.
const EncodeBurstSeconds = 10

// StatsPeriod is how often a reader states its own progress, which is the unit every
// confidence window counted in samples is measured in. Exported for the same reason
// BackoffMax is: the playback gate holds for a derived number of these blocks before it
// lets a stated speed convict a link, and a hold counted in blocks whose duration lives
// in an argument builder is a judgement whose calibration nobody can read.
//
// Half a second is ffmpeg's own default, kept rather than chosen so the flag renders what
// the reader would have done anyway. It is spelled on the command line all the same,
// because a default is not a contract: a future ffmpeg that reported once a second would
// silently halve the evidence behind every deliverability verdict.
const StatsPeriod = 500 * time.Millisecond

// Pace is how fast a reader may consume its input: a multiple of realtime, plus how
// much of the stream it may take at wire speed before that multiple binds. The zero
// value is wire speed throughout, which is what an unpaced read is.
type Pace struct {
	// Realtime is the media seconds per wall-clock second the read is allowed. It is
	// also the headroom any judgement about deliverability is made against, which is
	// why it is a number here and a string only at the moment it is rendered: a source
	// paced at exactly 1.0 and delivering 1.0 has answered nothing about the link,
	// while one allowed 2.0 and delivering 0.39 has answered everything.
	Realtime float64

	// Burst is how much of the stream may be read at wire speed before Realtime binds.
	Burst time.Duration
}

// The paces castor reads a source at, both per source nature and both shared by
// every reader that paces at all. VOD bursts at wire speed and then reads at twice
// realtime, which keeps a reader well ahead of 1x playback without pulling a whole
// movie in a couple of minutes. A live edge cannot be outrun, and the same burst
// only asks a CDN for segments that do not exist yet and trips its rate limiter.
var (
	paceVOD  = Pace{Realtime: 2.0, Burst: 90 * time.Second}
	paceLive = Pace{Realtime: 1.0}
)

// EncodePace paces the subtitle-burning encoder just above realtime. It is a read of
// the local spool rather than of an origin, and it lives beside the network paces
// because the playback gate's transcription lead is derived from its burst and that
// derivation must not reach into an argument builder to find it.
//
// The multiple must not be exactly 1.0: at dead-even playback speed the renderer's
// buffer has no steady-state headroom, so any encode or network jitter permanently
// erodes the initial preroll, and because the encoder never runs ahead it can never
// rebuild it. A slight margin lets the encoder's output spool accumulate a lead the
// renderer can draw from. It stays well under the puller's 2x, so the encode never
// overtakes whisper's committed frontier (the gate guarantees a lead before playback
// opens).
var EncodePace = Pace{Realtime: 1.15, Burst: EncodeBurstSeconds * time.Second}

// transient is the status set a dropped read is retried on: the answers an origin gives
// about ITSELF, which the very next request can get past. A rate-limited CDN answers 429
// and pairing that with a minute of backoff is what lets it be waited out instead of the
// HLS demuxer burning through segment numbers that all fail and keeping the IP tarpitted.
// The 5xx class is the same event with a different number on it: an edge answering one
// mid-stream segment 502/503/504 (or 500, which a CDN also emits for an origin fetch it
// could not complete) has said nothing about the next segment, and failing the whole read
// on it throws away a title over one bad request.
//
// 501 and 505 are deliberately absent, and so is every refusal (401, 403, 404, 410, the
// statuses internal/source/resolve/ffprobe classifies as ReachRefused). Those are answers
// about the REQUEST: an unsupported method and a spent signed link do not become supported
// or unspent by being asked again, and retrying them for a full backoff ceiling each spends
// a viewer's time reaching a conclusion the first answer already gave.
var transient = []int{429, 500, 502, 503, 504}

// segmentOpenRetries is how many times a segment whose OPEN failed is re-fetched before
// ffmpeg's HLS demuxer gives up on it and skips to the next one ("Segment N of playlist 0
// failed too many times, skipping", which at ffmpeg's own default of zero is what the FIRST
// failed open prints: the fragment is simply gone, and on an fMP4 program that is a hole in
// the picture no downstream copy can fill).
//
// Three, and both ends of that are derived rather than picked. It has to be more than one
// because the fault it covers is the one that is gone by the next request (a connection
// reset, a DNS blip, a status outside the set above), and each retry here is an immediate
// re-open: the waiting lives in the HTTP protocol's own reconnect ladder, which Backoff
// bounds, so a budget spent on a fast-failing open costs round trips and not minutes. It
// does not need to be more than three because nothing about a segment that has refused
// three immediate re-fetches suggests a fourth lands, and because the retries cannot outrun
// the judgement over the read either way: a read is convicted on delivering no BYTES for
// the derived stall window, whatever the demuxer is busy doing while it delivers none.
const segmentOpenRetries = 3

// Shape is what castor knows about how a source has to be fetched. Every field comes
// from what the source itself published (see media.Origin, established from a
// document the reader is about to open anyway), never from the URL string and never
// from a site.
//
// A zero Shape is the honest answer for a source whose documents castor never read,
// and every field reads unknown as no: not segmented, framing unknown, not live. The
// row that answers it is therefore the careful one rather than the permissive one.
type Shape struct {
	// Segmented reports that the program arrives as many small files rather than one
	// long read. It is the first fact a read policy keys on, because a per-read
	// deadline that is right for one long GET is a segment-abandoning timer on a
	// playlist.
	Segmented bool

	// Framing is how the segments carry their decoder configuration, from the chosen
	// playlist's EXT-X-MAP. Out-of-band means fMP4 fragments, which is the one shape
	// where abandoning a read mid-fragment is worse than waiting: a truncated AVCC
	// stream desyncs the h264_mp4toannexb filter a copy into MPEG-TS cannot do without.
	// FramingUnknown means no document said so, and a rule reading it must treat
	// unknown as unknown.
	Framing media.Framing

	// Live reports that the source has no end, so it arrives at 1x and cannot be
	// outrun whatever a reader is allowed.
	Live bool
}

// ShapeOf reads the fetch-relevant facts off what the source published. It is a
// projection and not a copy: media.Origin also carries the rendition ladder and the
// runtime, which are facts about what to read rather than about how, and a policy
// row keying on them would be answering someone else's question.
func ShapeOf(o media.Origin) Shape {
	return Shape{Segmented: o.Segmented, Framing: o.Framing, Live: o.Live}
}

// String names the shape for a log line and for the error a shape no row matched
// produces, where it is the only material anyone has to write the missing row from.
func (s Shape) String() string {
	return fmt.Sprintf("segmented=%t framing=%s live=%t", s.Segmented, s.Framing, s.Live)
}

// Policy is how one source is read: a value chosen from the source's shape and then
// RENDERED by whichever process fetches the bytes. It is the relationship the ffmpeg
// adapter already has with media.FormatInfo, and it is what lets both network readers
// prove they open an upstream on identical terms by comparing one value instead of
// two flag lists.
type Policy struct {
	// Name and Why identify the row this came from. Both are filled in by the table
	// rather than by the row's own body, so a row cannot mislabel itself, and both are
	// logged: a cast that read its source cautiously and a cast that read it fast are
	// otherwise indistinguishable after the fact.
	Name string
	Why  string

	// Deadline is how long ONE read may stall before ffmpeg abandons it and reconnects
	// (-rw_timeout). Zero withholds it entirely, which is a real answer rather than a
	// missing one: on a source whose fragments must arrive whole, abandoning a read
	// mid-fragment corrupts the stream fatally (see the segment-fragile row), and the
	// silence it guarded is instead observed by the party that can name it, castor's own
	// stall judgement over the bytes the read has landed.
	Deadline time.Duration

	// SegmentRetries is how many times a segment whose OPEN failed is re-fetched before the
	// reader skips it (-seg_max_retry, an option of ffmpeg's HLS demuxer, so it renders only
	// where the source castor opens is a playlist).
	//
	// It is a separate term from Deadline because it covers a separate failure, and the two
	// were long conflated: this one answers a segment castor never got a byte of, while the
	// deadline answers one whose read died partway through. Two field logs show them apart:
	// a failed open prints "Segment N of playlist 0 failed too many times, skipping", while
	// a mid-read timeout is a silent jump to the next segment with no line at all. So no
	// value here makes a withheld deadline safe, and withholding the deadline is no reason
	// to leave this at ffmpeg's zero.
	SegmentRetries int

	// Backoff is the ceiling on how long a fetch may keep retrying one read before it
	// fails (-reconnect_delay_max). Zero means a dropped read is not retried at all,
	// so it also withholds the retry flags: a status set to reconnect on says nothing
	// when reconnecting is off.
	Backoff time.Duration

	// RetryStatuses are the HTTP answers a dropped read is retried on rather than
	// failed on (-reconnect_on_http_error). It is a list because the transient class is
	// not one code: a CDN answering a mid-stream segment with 503 is the same event as
	// one answering 429.
	RetryStatuses []int

	// Pace is how fast the source may be consumed. Whether a given reader applies it at
	// all is that reader's own business (a single long GET spooled from byte 0 is
	// throttled by nothing), but what the pace IS belongs to the source's nature.
	Pace Pace
}
