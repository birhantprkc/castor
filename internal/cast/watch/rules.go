package watch

import (
	"fmt"
	"slices"
)

// party is who a verdict is about, which is what decides whose evidence explains it. It
// is a column on the row rather than a switch over Kind, so a rule that blames the
// renderer can never be answered with the reader's stderr.
type party int

const (
	// theProducer is the zero value because most of what goes wrong with a cast goes
	// wrong upstream, and because its evidence is the expensive one to forget: a reader
	// castor killed never ran its own error path, so those lines exist nowhere else.
	theProducer party = iota
	theRenderer
)

// Rule is one row of the health table: which windows it answers, why, the facts it
// fires on, and the verdict it reaches.
type Rule struct {
	// Name identifies the row in a log line, and is what a Health no row matched is
	// reported against.
	Name string
	// Why is the reasoning, carried out to the caller so a cast that was abandoned says
	// why where a user can read it rather than only in this file.
	Why string
	// Windows are the windows this row answers. It is a field and not a second table
	// because the pre-playback rules and the in-flight ones are the same rules: what
	// differs is what may be DONE about them, which is the action table's business.
	Windows []Window
	// When reports whether this row answers a cast in this state. It reads Health and
	// nothing else, which is what makes every row exercisable with no ffmpeg, no network
	// and no renderer.
	When func(Health) bool
	// Kind is the verdict.
	Kind Kind
	// Blames is whose evidence explains this verdict.
	Blames party
}

// rules is how castor judges a cast, in order, first match. It is walked by Watch,
// which is the only way a verdict is reached.
//
// The rows run from the states that end a cast to the states that continue it, and the
// last two rows are the totals: a Health no row answered would be an error, so every
// window ends in a row that always matches. Adding a pathology is one row here plus one
// case in rules_test.go, where the case builds a Health directly.
var rules = []Rule{{
	// Any read failure before playback starts is fatal: casting whatever fragment made
	// it into the buffer would play a few seconds and stop mid-scene, which reads as a
	// worse failure than a clear error. After playback starts the buffer keeps serving
	// and a read error only truncates the tail, which is why this row does not answer
	// the playing window: the reader's terminal error travels out through the encoder's
	// input and is joined into the cast's result there.
	Name:    "read-failed",
	Why:     "the source read reached a terminal error before playback could start",
	Windows: []Window{BeforePlay},
	When:    func(h Health) bool { return h.Failed },
	Kind:    Dead,
}, {
	// The producer's output ended and the artifact a renderer would have been pointed at
	// was never written. If nothing came out of it, nothing ever will, and saying so now
	// is what keeps a dead attempt from being handed to a renderer: a container refuses a
	// track it cannot carry at header-write time, before a single byte, and an ADTS AAC
	// copy into the mp4 muxer exits 255 having written audio:0KiB.
	Name:    "produced-nothing",
	Why:     "the producer ended without writing anything a renderer could fetch",
	Windows: []Window{Opening},
	When:    func(h Health) bool { return h.Ended && !h.playable() },
	Kind:    Dead,
}, {
	// A producer that has stopped delivering, and a renderer with nothing left to play out
	// of what it already delivered. Both terms are required, and the second is the
	// difference between the tail of a title and the whole of it: the read is allowed twice
	// realtime, so at the one-hour mark it is typically half an hour ahead of the viewer,
	// and an upstream that goes quiet there has cost the ending and not the film. Firing on
	// the silence alone tore down the encoder and closed the server thirty minutes of
	// watchable media early, and then reported an expired playlist as the reason.
	//
	// Before playback nothing has been delivered to anybody, so the second term is vacuous
	// there and this stays exactly what it was: the dead playlist whose segments all answer
	// 403, named while the attempt can still be revised.
	Name:    "stalled",
	Why:     "the producer stopped delivering entirely and the renderer has played everything that reached it; the likeliest cause is a signed playlist whose segments have expired (they answer 404), and re-extracting the link is what gets a fresh token",
	Windows: []Window{BeforePlay, Playing},
	When:    func(h Health) bool { return !h.Ended && h.SinceGrowth > StallWindow && !h.buffered() },
	Kind:    Stalled,
}, {
	// The pre-playback arm of the deliverability rule, and the reason the gate holds at
	// all. The deficit must outlast the backoff ceiling this read was handed, for the
	// same reason the stall window and the in-flight arm below are derived from it: a
	// rate-limited CDN answers 429 and goes quiet for a full ceiling before the retry
	// that lands, and a judgement that fires inside that window is causing the deficit it
	// claims to be observing. One ceiling here against two in flight, because nothing is
	// watching yet: the answer is to revise the attempt rather than to end a cast someone
	// is watching, so it is worth reaching sooner.
	//
	// Firing on the first sample is what a single 429 or a whisper model loading on the
	// goroutine draining the PCM tee looked like: a cast refused, and then every other
	// admitted link walked and burned for a fault none of them caused.
	Name:    "undeliverable",
	Why:     "the source has delivered fewer media seconds per wall-clock second than playback consumes for longer than a reconnect ceiling, so the cast can never catch up however long it is given",
	Windows: []Window{BeforePlay},
	When:    func(h Health) bool { return h.starving() && h.SinceDeficit > deficitWindow },
	Kind:    Undeliverable,
}, {
	// A deficit that has not yet outlasted that ceiling HOLDS the gate: it neither
	// convicts nor opens. Both halves are load-bearing, and this row sits above every
	// readiness row for the second one.
	//
	// Holding rather than convicting is what lets a transient dip be a dip. Holding
	// rather than opening is the whole of decision 3: past this point the deliverability
	// question is unreachable, because a revision is what would rescue a starving cast
	// and no revision exists once a renderer holds a URL. A cast castor has measured
	// under playback rate is one it is still deciding about, not one to hand over.
	//
	// The wait is bounded by the row above at one ceiling, and it costs a healthy cast
	// nothing: a read allowed 2x with a wire-speed burst reports multiples of realtime
	// (a real one measured 2.100x) and never matches this at all.
	Name:    "under-playback-rate",
	Why:     "the read is delivering less than playback consumes, and the deficit has not yet outlasted the backoff this read was handed",
	Windows: []Window{BeforePlay},
	When:    Health.starving,
	Kind:    Starting,
}, {
	// The in-flight arm. Same measurement, but a viewer is watching, so the deficit must
	// outlast two reconnect ceilings before it is acted on, for the same reason the stall
	// window does: a rate-limited CDN goes quiet for a full ceiling and then lands its
	// retry, and a judgement that fires inside that window converts a legitimate backoff
	// into a failed cast.
	//
	// Because the reader's figure is cumulative from its own start, this arm convicts
	// exactly the read whose whole life has fallen under playback rate, which is the
	// condition under which the renderer's buffer must drain. A read that was healthy
	// for an hour and then stops dead is named by the stall row above, sooner.
	Name:    "undeliverable-in-flight",
	Why:     "the source has been delivering less than playback consumes for longer than two reconnect ceilings, so the renderer's buffer cannot be refilled",
	Windows: []Window{Playing},
	When:    func(h Health) bool { return h.starving() && h.SinceDeficit > StallWindow },
	Kind:    Undeliverable,
}, {
	// The renderer accepted Play and never came for the bytes. Without this the cast is
	// reported as SUCCESS: castor encodes the whole title, the sink's idle grace expires
	// with no client having ever arrived, and Wait returns nil.
	//
	// Never fetched is the whole of what this window can say about a renderer, and the
	// boundary is deliberate. A renderer that fetched and then STOPPED is not judged here at
	// all: from outside, a viewer who paused, a viewer who walked away and a renderer that
	// crashed are one and the same fact (an absence of requests), and the buffer cannot
	// separate them either, because on the one supervised composition the encoder keeps
	// filling the delivery from the spool for the rest of the title, so the media a renderer
	// still has in hand only grows. A silent renderer is answered by the delivery's write
	// deadline instead (see replay's defaultWriteDeadline), and whether it ever took what was
	// made for it is stated once, at the end of the cast, where the answer is arithmetic
	// rather than a guess about how long people pause for (see core.Undelivered).
	Name:    "unfetched",
	Why:     "the renderer accepted the stream URL and never requested it",
	Windows: []Window{Playing},
	When:    func(h Health) bool { return h.Requests == 0 && h.SinceFetch > fetchWindow },
	Kind:    Unfetched,
	Blames:  theRenderer,
}, {
	// The "subs never show" bug as a rule: the burn-in encoder must never start ahead of
	// the transcription's committed frontier, so bytes alone do not open this gate.
	//
	// Bytes are required too: a read that dies instantly flips the transcription's Done
	// (its PCM hits EOF) before the read's error lands, and the gate must not open onto
	// an empty buffer in that window.
	Name:    "burn-in-ready",
	Why:     "the buffer holds media, the read has proved it can deliver it, and the transcription is far enough ahead of the encoder",
	Windows: []Window{BeforePlay},
	When: func(h Health) bool {
		return h.Subtitles && h.playable() && h.measured() && h.leads()
	},
	Kind: Ready,
}, {
	// An ended read opens this gate even empty: there is nothing left to wait for, and
	// the read's own error is read by the caller immediately after.
	Name:    "ready",
	Why:     "the buffer holds media and the read has proved it can deliver it",
	Windows: []Window{BeforePlay},
	When: func(h Health) bool {
		return !h.Subtitles && (h.playable() && h.measured() || h.Ended)
	},
	Kind: Ready,
}, {
	// Overdue is a real answer and not a timeout: a slow upstream can legitimately take
	// this long to yield a first byte, and the delivery is better off proceeding and
	// letting the renderer's own buffering wait.
	Name:    "artifact-ready",
	Why:     "the artifact a renderer fetches exists, or this delivery has waited as long as it is willing to",
	Windows: []Window{Opening},
	When:    func(h Health) bool { return h.playable() || h.Overdue },
	Kind:    Ready,
}, {
	Name:    "starting",
	Why:     "nothing has been established yet",
	Windows: []Window{BeforePlay, Opening},
	When:    func(Health) bool { return true },
	Kind:    Starting,
}, {
	Name:    "healthy",
	Why:     "nothing is against this cast",
	Windows: []Window{Playing},
	When:    func(Health) bool { return true },
	Kind:    Healthy,
}}

// judge walks the table for one window and returns the row that answers this state
// together with what to do about it.
//
// Both failures are errors naming the shape rather than a fall-through, because a
// silent default here is a cast nobody is judging: an unanswered Health means the totals
// stopped being total, and a verdict with no action for its window means a row was given
// a window the action table was never told about.
func judge(w Window, h Health) (Rule, action, error) {
	for _, r := range rules {
		if !slices.Contains(r.Windows, w) || !r.When(h) {
			continue
		}
		act, ok := actions[verdict{Kind: r.Kind, Window: w}]
		if !ok {
			return Rule{}, 0, fmt.Errorf("rule %q reached a %s verdict in the %s window, which has no action", r.Name, r.Kind, w)
		}
		return r, act, nil
	}
	return Rule{}, 0, fmt.Errorf("no health rule answers a cast in the %s window with %s", w, h)
}
