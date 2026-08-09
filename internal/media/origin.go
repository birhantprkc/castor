package media

import (
	"cmp"
	"net/url"
	"slices"
	"time"
)

// This file is what castor knows about the far end of a cast: what the origin said
// when it was asked (Reach), what the source publishes about the program it serves
// (Origin, Rendition), and what is arriving while it is being read (Progress,
// Speed). None of it is a capability of a renderer and none of it is a knob: a
// link's carrying capacity is a measured fact about that link, which is precisely
// why it lives here and not on Renderer.

// Reach is how far an attempt to read a source got, as a fact about the ORIGIN
// rather than about the media: it answers "did anybody answer, and what did they
// say", where ProbeInfo answers "what is inside it".
//
// It exists because the two ways a measurement fails call for opposite decisions
// and used to arrive as one indistinguishable error. An origin answering 403
// answers it to every reader, so a candidate holding one is worth nothing to the
// puller either. A probe castor killed at its own deadline established nothing
// about the source: the reader that follows it gets reconnects, a wider retry
// status set and minutes where ffprobe had seconds, so that link can still land.
//
// Collapsing the two is what produced "best stream selected bitrate=0 height=0"
// on a run whose every other candidate was a hard-rejected decoy: the survivor was
// a link castor had already killed its own probe against, and the cast then spent
// minutes failing to read it. Collapsing them the other way is just as expensive,
// which is why this is a three-value fact and not a bool: a rate limiter answering
// a burst of probes castor itself fired must not be allowed to convict every
// candidate behind that signature.
type Reach int

const (
	// ReachUnproven is the zero value on purpose, because it is the lenient answer:
	// a Reach nobody established can then never be the reason a candidate is
	// dropped. Timeouts, connection resets, 429s and 5xx all land here, and so does
	// any failure the classifier does not recognise, since the only material for a
	// finer answer is the reader's own prose and prose is not a contract. A wording
	// change may cost a log line its sharpness; it may never cost a cast.
	ReachUnproven Reach = iota
	// ReachOpened is the origin serving the source: it answered, and the reader read
	// what came back. Nothing about the media follows from it, only that the link is
	// live.
	ReachOpened
	// ReachRefused is the origin answering with a final no (401, 403, 404, 410). A
	// spent signed link answers this, and no amount of reconnecting changes it: only
	// a fresh extraction does.
	ReachRefused
)

// String names the reach for a log line. Unproven is the default arm because it is
// the bucket every unrecognised outcome already falls into.
func (r Reach) String() string {
	switch r {
	case ReachOpened:
		return "opened"
	case ReachRefused:
		return "refused"
	default:
		return "unproven"
	}
}

// Ladder is what a captured document's own grammar says about renditions: whether it
// enumerates several to choose between (an HLS master's #EXT-X-STREAM-INF tags) or is
// itself one rendition. It is read from the body the browser has already downloaded,
// so it costs no request against the origin, ages no signed link, and needs no
// knowledge of any site: the format's own tags are the whole of it.
//
// It is a fact about the document at the URL as it was CAPTURED. Once a playlist has
// actually been read, Origin.Renditions and Origin.Sole are the authority, and a
// stream narrowed from a master to one of its rungs still carries the Ladder of the
// master it came from. Nothing may re-derive this from a URL.
//
// It exists because "is this a master" was answered from path substrings, so a master
// served as index.m3u8 answered no. Three runs in a row selected a single media
// playlist as their best stream, reported renditions=1 sole=true, and had nothing to
// offer when the link then delivered 0.39x realtime: every recovery that wanted a
// lighter rung found no ladder to walk.
type Ladder int

const (
	// LadderUnknown is the zero value because reading the body is best-effort and must
	// stay lenient in exactly the way Reach is: a redirect carries no body, a body
	// Chrome evicted cannot be handed over, and a request that never finished has none
	// to give. All three establish nothing, and nothing established may cost a
	// candidate its place. Unknown is never "not a master". A document that read as no
	// playlist at all (a CDN error page, an HTML interstitial) lands here too, because
	// its silence about renditions is not a statement about the source.
	LadderUnknown Ladder = iota
	// LadderMultivariant is a document advertising renditions: a master, carrying the
	// rungs a recovery degrades within.
	LadderMultivariant
	// LadderSole is a playlist advertising no renditions: it IS the rendition, so what
	// it carries is the whole of what the source offers at that URL.
	LadderSole
)

// String names the ladder for a log line. Unknown is the default arm because it is
// where every failed and every unattempted reading already lands.
func (l Ladder) String() string {
	switch l {
	case LadderMultivariant:
		return "multivariant"
	case LadderSole:
		return "sole"
	default:
		return "unknown"
	}
}

// Bitrate is a rate in bits per second. It is a type rather than a bare int64
// because the two rates a cast holds are not interchangeable: what a source
// DECLARED for a rendition it is offering, and what a reader MEASURED coming down
// the wire. Only the first is ever a ceiling to choose under, and only the second
// is evidence about the link.
type Bitrate int64

// Rendition is one version of a program a source offered, as the source described
// it. Nothing here is measured, so nothing here is proof: a declared BANDWIDTH is a
// publisher's claim about a rung of its own ladder.
type Rendition struct {
	URL *url.URL

	// Bitrate is the rate the source declared (HLS BANDWIDTH). 0 means it declared
	// none, which is not the same as cheap: see Origin.Lighter, where an undeclared
	// rate is refused as evidence rather than read as zero.
	Bitrate Bitrate

	// Height is the declared display height (HLS RESOLUTION), 0 when the source
	// omitted it. An unknown height stays eligible under any cap on purpose: a source
	// that declares nothing is trusted rather than discarded.
	Height int
}

// Origin is what the source itself publishes about the program castor chose to
// read: the renditions it offered, how its segments are framed, whether it ever
// ends, whether it is encrypted, and how long it runs. Every field is harvested
// from a document the reader is about to open anyway (an HLS playlist), so none of
// it costs a measurement and none of it is a guess about a site.
//
// It exists because castor had no value at all for "there was nothing to choose
// from". A media playlist normalises to one synthetic rendition, which is the right
// shape for selection and hid the difference that mattered: a source publishing a
// single 3840x1600 rendition at 17 Mbit/s was indistinguishable from a master
// castor had capped at 1080. That run read the 4K rung to serve a 2 Mbit/s
// re-encode, and nothing in it could say the cap had never been achievable. Sole()
// is that difference, and it is the same fact a recovery needs before it offers to
// drop a rung.
//
// A zero Origin means no document was read: the source is not segmented, or the
// fetch was refused. Zero therefore means unknown for every field here and never
// "no": Framing is FramingUnknown rather than in-band, and Duration 0 is "the
// source did not say", which is why ProjectedRuntime refuses to answer over it.
type Origin struct {
	// Renditions is the ladder the source offered, in publication order, reduced to
	// the rungs that carry video (an audio-only rung is not something to fall back
	// to). It survives past the choice made from it, which is the whole point: the
	// selection used to be the only thing that outlived the document.
	Renditions []Rendition

	// Segmented reports that the program arrives as many small files rather than one
	// long read. It is the first fact a read policy keys on: a per-read deadline that
	// is right for one long GET is a segment-abandoning timer on a playlist.
	Segmented bool

	// Framing is how the SEGMENTS carry their decoder configuration, taken from the
	// chosen document's EXT-X-MAP: a Media Initialization Section is what fMP4
	// segments need and MPEG-TS segments never have. FramingUnknown means no document
	// said, so a rule reading this must treat unknown as unknown.
	Framing Framing

	// Live reports that the source has no end: an HLS media playlist with no
	// EXT-X-ENDLIST is still being appended to.
	//
	// It is established by ONE witness and never by a vote, because the two that exist are not
	// comparable: the document listing the segments knows, while a probe reporting no duration
	// is proof of nothing (see resolve, which establishes it, and watch.Health, which is what a
	// wrong answer here switches off).
	Live bool

	// Encrypted reports that segments are delivered under an EXT-X-KEY with a real
	// method. It is a fact about the read (a key fetch per key change, on the same
	// signed session), not a refusal: castor reads what ffmpeg can decrypt.
	Encrypted bool

	// Duration is the program's runtime as the source published it, 0 when it did not.
	// For a VOD playlist it is the EXTINF sum, which is the number ffprobe routinely
	// cannot report for a playlist at all, and it is the numerator of every honest
	// statement about how long a cast will take.
	Duration time.Duration
}

// Sole reports that the source gave castor no choice: it published one rendition,
// or none that could be read. It is the negation of "there was a variant list",
// and it is what turns a recovery's offer to degrade into an honest refusal instead
// of a rung that does not exist.
func (o Origin) Sole() bool { return len(o.Renditions) < 2 }

// Lighter returns the renditions cheaper than a ceiling, heaviest first, so a
// caller degrading under a measured link speed takes the head and gets the most
// picture that link can carry.
//
// A rendition whose bitrate the source never declared is NOT lighter. Arithmetic
// says 0 is below every ceiling, and acting on that is how a degrade lands on a
// rung heavier than the one it was escaping: an undeclared rate is the absence of
// evidence, and the whole reason to move is evidence about what the link can carry.
func (o Origin) Lighter(than Bitrate) []Rendition {
	out := make([]Rendition, 0, len(o.Renditions))
	for _, r := range o.Renditions {
		if r.Bitrate > 0 && r.Bitrate < than {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Rendition) int { return cmp.Compare(b.Bitrate, a.Bitrate) })
	return out
}

// Speed is media seconds delivered per wall-clock second, which is ffmpeg's own
// speed= field. It is the one throughput measure that needs no declared bitrate,
// and that is why a cast is judged on it: the sources that starve a cast are
// exactly the ones whose BANDWIDTH the publisher omits and whose format bit_rate
// ffprobe cannot report, so bytes per second have nothing to be compared against.
// Media seconds per wall second compare against 1.
//
// The number that named a real failure was 0.109: a read delivering eleven minutes
// of picture per hour of waiting, which no amount of patience turns into playback.
type Speed float64

// Progress is one sample of what a reader has delivered so far. Three fields
// because that is what separates "nothing is arriving" from "something is arriving
// too slowly to watch", and those two call for different responses: position and
// speed answer the second, bytes answer the first (a reader that has produced no
// parseable media position has still moved bytes).
type Progress struct {
	Position time.Duration
	Bytes    int64
	Speed    Speed
}

// ProjectedRuntime reports how long delivering the whole program takes at a
// measured speed, which is the arithmetic that turns "the cast is slow" into a
// number a user can act on: a two hour title at 0.109x is a little over eighteen
// hours, and saying so is the difference between abandoning a cast and being
// abandoned by it.
//
// It refuses rather than guesses, on three counts. A source that published no
// duration (a playlist castor never read) has no runtime to project, and a speed of
// zero projects everything to forever. A live edge is refused on its own account
// rather than by way of its duration: a sliding window's EXTINF sum is the length of
// the window and not of the program, so the one shape that could put a plausible
// duration on a stream that never ends is exactly the one where dividing it by a
// speed produces a confident, meaningless number.
func (o Origin) ProjectedRuntime(at Speed) (time.Duration, bool) {
	if o.Live || o.Duration <= 0 || at <= 0 {
		return 0, false
	}
	return time.Duration(float64(o.Duration) / float64(at)), true
}
