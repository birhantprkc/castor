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
	// and no renderer. It is nil on a total row, which is never asked (see total).
	When func(Health) bool
	// Kind is the verdict.
	Kind Kind
	// Blames is whose evidence explains this verdict.
	Blames party
}

// rules is how castor judges a cast, in order, first match. It is walked by Watch,
// which is the only way a verdict is reached.
//
// The rows run from the states that end a cast to the states that continue it, and what
// none of them claims is answered by the window's own total row (see total): a judgement
// is a verdict rather than a value-or-error, because a cast nobody could judge is a cast
// nobody is watching. Adding a pathology is one row here plus one case in rules_test.go,
// where the case builds a Health directly.
//
// EVERY ROW NAMES THE PRODUCTION MONITOR IT IS REACHED THROUGH, as the file and the FUNCTION
// that opens it, and that is not documentation: a row is only worth its place if the shipping
// program can reach it, and a Health built by hand proves nothing about that. A rule keyed on a
// fact no monitor in its window fills reads that fact's zero value on every real cast, so it
// fails nothing while reading as coverage of a pathology nobody is judging. The citations are
// resolved and checked against the wiring (see TestEveryRuleNamesTheProductionMonitorThatReachesIt);
// a row that cannot name one is either mis-scoped or dead, and the answer to a dead row is
// to delete it.
//
// The function and not the line: a citation into two other packages that named a line number
// broke on every unrelated edit above it, and repairing this file by re-typing line numbers is
// how a checked citation turns into a stale one.
var rules = []Rule{{
	// Any read failure before playback starts is fatal: casting whatever fragment made
	// it into the buffer would play a few seconds and stop mid-scene, which reads as a
	// worse failure than a clear error. After playback starts the buffer keeps serving
	// and a read error only truncates the tail, which is why this row does not answer
	// the playing window: the reader's terminal error travels out through the encoder's
	// input and is joined into the cast's result there.
	//
	// Production path: the playback gate over the source read (pipeline/gate.go:waitForPlayable).
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
	//
	// Production path: the artifact gate every delivery mechanism is opened behind, which is the
	// only place a producer is watched with nobody yet pointed at what it makes
	// (core/deliver.go:ready).
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
	//
	// It is also the only bound a read with no mid-read deadline has, which is what let that
	// deadline be withheld from a source whose fragments must arrive whole (see read's
	// segment-fragile row). Such a read can sit on a socket that was accepted and then went
	// quiet indefinitely, so this row is what ends it, at StallWindow of no bytes landing,
	// with the reader's own stderr attached. Anything that makes the buffer term bind before
	// playback, or that shortens this window towards the reconnect ceiling the reader was
	// handed, takes that bound away.
	//
	// Production path: the playback gate (pipeline/gate.go:waitForPlayable) and the playing cast, on
	// both compositions that serve one: the leg with a read of its own (pipeline/gate.go:supervise)
	// and the leg whose encode IS the read, judged by the delivery driver that started it
	// (core/deliver.go:watchTheEncode). The buffer term is supplied by those two alone, which is
	// what makes it vacuous before playback rather than merely unused there.
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
	//
	// Production path: the playback gate (pipeline/gate.go:waitForPlayable), whose pace is the read's own
	// answer about itself.
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
	// rather than opening is why the hold exists at all: past this point the deliverability
	// question is unreachable, because a revision is what would rescue a starving cast
	// and no revision exists once a renderer holds a URL. A cast castor has measured
	// under playback rate is one it is still deciding about, not one to hand over.
	//
	// The wait is bounded by the row above at one ceiling, and it costs a healthy cast
	// nothing: a read allowed 2x with a wire-speed burst reports multiples of realtime
	// (a real one measured 2.100x) and never matches this at all.
	//
	// Production path: the playback gate (pipeline/gate.go:waitForPlayable).
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
	//
	// Production path: the playing cast (pipeline/gate.go:supervise).
	Name:    "undeliverable-in-flight",
	Why:     "the source has been delivering less than playback consumes for longer than two reconnect ceilings, so the renderer's buffer cannot be refilled",
	Windows: []Window{Playing},
	When:    func(h Health) bool { return h.starving() && h.SinceDeficit > StallWindow },
	Kind:    Undeliverable,
}, {
	// The renderer accepted Play and never came for the bytes. Without this the cast is
	// reported as SUCCESS: castor encodes the whole title, the delivery's idle grace expires
	// with no client having ever arrived, and Wait returns nil.
	//
	// Keyed on what the renderer was HANDED and not on whether it asked: a request count says
	// "it fetched" about a renderer that came to the door and took nothing, which is the one
	// shape this verdict exists for (Consumer states why bytes are the only honest measure).
	//
	// Handed nothing is the whole of what this window can say about a renderer, and the
	// boundary is deliberate. A renderer that took some of it and then STOPPED is not judged here
	// at all: from outside, a viewer who paused, a viewer who walked away and a renderer that
	// crashed are one and the same fact (nothing more being taken), and the buffer cannot
	// separate them either, because on the one supervised composition the encoder keeps
	// filling the delivery from the spool for the rest of the title, so the media a renderer
	// still has in hand only grows. A silent renderer is answered by the delivery's write
	// deadline instead (see replay's DefaultWriteDeadline), and whether it ever took what was
	// made for it is stated once, at the end of the cast, where the answer is arithmetic
	// rather than a guess about how long people pause for (see core.Undelivered).
	//
	// Production path: the playing cast, in both shapes it has (pipeline/gate.go:supervise and
	// core/deliver.go:watchTheEncode), which are the only monitors handed what the delivery knows
	// about the renderer's fetching.
	Name:    "unfetched",
	Why:     "the renderer accepted the stream URL and was never handed a byte of what this cast produced for it",
	Windows: []Window{Playing},
	When:    func(h Health) bool { return h.Handed == 0 && h.SinceFetch > fetchWindow },
	Kind:    Unfetched,
	Blames:  theRenderer,
}, {
	// The "subs never show" bug as a rule: the burn-in encoder must never start ahead of
	// the transcription's committed frontier, so bytes alone do not open this gate.
	//
	// Bytes are required too: a read that dies instantly flips the transcription's Done
	// (its PCM hits EOF) before the read's error lands, and the gate must not open onto
	// an empty buffer in that window.
	//
	// Production path: the playback gate (pipeline/gate.go:waitForPlayable), the only monitor a transcription
	// is handed to.
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
	//
	// Production path: the playback gate (pipeline/gate.go:waitForPlayable).
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
	//
	// Production path: the artifact gate (core/deliver.go:ready), over the patience each mechanism
	// states for itself: the stream delivery has one to run out of, and the segmented one states
	// none and so opens on the playlist alone.
	Name:    "artifact-ready",
	Why:     "the artifact a renderer fetches exists, or this delivery has waited as long as it is willing to",
	Windows: []Window{Opening},
	When:    func(h Health) bool { return h.playable() || h.Overdue },
	Kind:    Ready,
}}

// starting is the total row of both pre-playback windows: nothing has been established, so
// there is nothing to act on and the watch takes another reading.
//
// Production path: every monitor opened before a renderer holds a URL
// (pipeline/gate.go:waitForPlayable, core/deliver.go:ready).
var starting = Rule{
	Name:    "starting",
	Why:     "nothing has been established yet",
	Windows: []Window{BeforePlay, Opening},
	Kind:    Starting,
}

// healthy is the total row of the playing window: a cast in flight with nothing against it.
//
// Production path: the playing cast (pipeline/gate.go:supervise,
// core/deliver.go:watchTheEncode).
var healthy = Rule{
	Name:    "healthy",
	Why:     "nothing is against this cast",
	Windows: []Window{Playing},
	Kind:    Healthy,
}

// total is the row a window ends in: what a cast is judged as when no ordered row above
// claimed it. It is a function over the window rather than two more rows at the bottom of
// the table because that is what makes the walk total by construction: every Window value
// has an answer here, so judge reaches a verdict instead of reporting that the totals
// stopped being total. Neither row carries a When, since neither is ever asked one.
func total(w Window) Rule {
	if w == Playing {
		return healthy
	}
	return starting
}

// judge walks the table for one window and returns the row that answers this state
// together with what to do about it.
//
// The one failure left is a verdict with no action for its window, which means a row was
// given a window the action table was never told about: a cast being observed by something
// with no idea what to do. It is an error naming the row rather than a fall-through for the
// same reason the rest of this file has none.
func judge(w Window, h Health) (Rule, action, error) {
	r := total(w)
	for _, candidate := range rules {
		if slices.Contains(candidate.Windows, w) && candidate.When(h) {
			r = candidate
			break
		}
	}
	act, ok := actions[verdict{Kind: r.Kind, Window: w}]
	if !ok {
		return Rule{}, 0, fmt.Errorf("rule %q reached a %s verdict in the %s window, which has no action", r.Name, r.Kind, w)
	}
	return r, act, nil
}
