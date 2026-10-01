package watch

import (
	"fmt"
	"slices"
)

type action int

const (
	// keepWatching re-reads the facts on the next tick.
	keepWatching action = iota
	// open ends the wait successfully.
	open
	revise
	// abandon ends the cast with the verdict's reasoning and the measurements behind it.
	abandon
)

// verdict is the pair the action table is keyed on: what was judged, and where.
type verdict struct {
	kind   Kind
	window Window
}

// actions per verdict/window; split is key safety (see TestNoPlayingVerdictRevises).
var actions = map[verdict]action{
	{kind: starting, window: BeforePlay}: keepWatching,
	{kind: starting, window: Opening}:    keepWatching,
	{kind: healthy, window: Playing}:     keepWatching,

	{kind: ready, window: BeforePlay}: open,
	{kind: ready, window: Opening}:    open,

	{kind: Dead, window: BeforePlay}:          revise,
	{kind: Dead, window: Opening}:             revise,
	{kind: Stalled, window: BeforePlay}:       revise,
	{kind: Stalled, window: Opening}:          revise,
	{kind: Undeliverable, window: BeforePlay}: revise,

	{kind: Stalled, window: Playing}:       abandon,
	{kind: Undeliverable, window: Playing}: abandon,
	{kind: Unfetched, window: Playing}:     abandon,
}

// party: verdict subject; determines whose evidence (not a Kind switch).
type party int

const (
	theProducer party = iota
	theRenderer
)

// rule: health table row (internal; caller sees Fault not row).
type rule struct {
	// name: row identifier in logs; used for unmatched Health.
	name string
	// why: reasoning, carried to caller for user readability.
	why string
	// windows: where row applies (action table decides what to do).
	windows []Window
	// when: health predicate; nil on the fallback rows, which are never asked.
	when func(Health) bool
	kind Kind
	// blames: whose evidence explains this verdict.
	blames party
}

// rules: judgment rules (first match, total fallback); add rule+test.
var rules = []rule{{
	// Pre-play read failure is fatal (partial buffer plays then stops).
	name:    "read-failed",
	why:     "the source read reached a terminal error before playback could start",
	windows: []Window{BeforePlay},
	when:    func(h Health) bool { return h.failed },
	kind:    Dead,
}, {
	// Producer ended with no output (e.g., container refuses codec).
	name:    "produced-nothing",
	why:     "the producer ended without writing anything a renderer could fetch",
	windows: []Window{Opening},
	when:    func(h Health) bool { return h.ended && !h.playable() },
	kind:    Dead,
}, {
	// A delivery with no patience of its own would otherwise wait on a silent upstream forever.
	name:    "produced-nothing-yet",
	why:     "the producer is still running but has written nothing a renderer could fetch for the whole stall window; the likeliest cause is an upstream that accepted the connection and never sent a byte",
	windows: []Window{Opening},
	when:    func(h Health) bool { return !h.ended && !h.playable() && h.sinceGrowth > StallWindow },
	kind:    Stalled,
}, {
	// Producer stopped, renderer exhausted buffer (likely expired playlist).
	name:    "stalled",
	why:     "the producer stopped delivering, or delivers under half of playback pace, and the renderer has played everything that reached it; the likeliest cause is a signed playlist whose segments have expired (they answer 404), and re-extracting the link is what gets a fresh token",
	windows: []Window{BeforePlay, Playing},
	when:    func(h Health) bool { return !h.ended && h.sinceGrowth > StallWindow && !h.buffered() },
	kind:    Stalled,
}, {
	// Deficit outlasted backoff ceiling: read too slow to catch up.
	name:    "undeliverable",
	why:     "the source has delivered fewer media seconds per wall-clock second than playback consumes for longer than a reconnect ceiling, so the cast can never catch up however long it is given",
	windows: []Window{BeforePlay},
	when:    func(h Health) bool { return h.starving() && h.sinceDeficit > deficitWindow },
	kind:    Undeliverable,
}, {
	// Deficit not yet outlasted backoff (gate holds, not answered in-flight).
	name:    "under-playback-rate",
	why:     "the read is delivering less than playback consumes, and the deficit has not yet outlasted the backoff this read was handed",
	windows: []Window{BeforePlay},
	when:    Health.starving,
	kind:    starting,
}, {
	// In-flight: the deficit outlasted the stall window while a viewer watches.
	name:    "undeliverable-in-flight",
	why:     "the source has been delivering less than playback consumes for longer than the stall window, so the renderer's buffer cannot be refilled",
	windows: []Window{Playing},
	when:    func(h Health) bool { return h.starving() && h.sinceDeficit > StallWindow },
	kind:    Undeliverable,
}, {
	// Renderer accepted stream but never fetched bytes.
	name:    "unfetched",
	why:     "the renderer accepted the stream URL and was never handed a byte of what this cast produced for it",
	windows: []Window{Playing},
	when:    func(h Health) bool { return h.handed == 0 && h.sinceFetch > fetchWindow },
	kind:    Unfetched,
	blames:  theRenderer,
}, {
	// Subtitles ready: buffer has media, read delivers, transcription leads.
	name:    "burn-in-ready",
	why:     "the buffer holds media, the read has proved it can deliver it, and the transcription is far enough ahead of the encoder",
	windows: []Window{BeforePlay},
	when: func(h Health) bool {
		return h.subtitles && (h.playable() && h.measured() && h.leads() || h.ended)
	},
	kind: ready,
}, {
	// Buffer has media, read delivers (or read ended).
	name:    "ready",
	why:     "the buffer holds media and the read has proved it can deliver it",
	windows: []Window{BeforePlay},
	when: func(h Health) bool {
		return !h.subtitles && (h.playable() && h.measured() || h.ended)
	},
	kind: ready,
}, {
	// Artifact exists or delivery timeout (first byte overdue).
	name:    "artifact-ready",
	why:     "the artifact a renderer fetches exists, or this delivery has waited as long as it is willing to",
	windows: []Window{Opening},
	when:    func(h Health) bool { return h.playable() || h.overdue },
	kind:    ready,
}}

// nothingEstablished answers both pre-playback windows when no rule did: nothing has been established.
var nothingEstablished = rule{name: "starting", why: "nothing has been established yet", kind: starting}

// nothingAgainst answers the playing window when no rule did: a cast in flight with nothing against it.
var nothingAgainst = rule{name: "healthy", why: "nothing is against this cast", kind: healthy}

func judge(w Window, h Health) (rule, action, error) {
	r := nothingEstablished
	if w == Playing {
		r = nothingAgainst
	}
	for _, candidate := range rules {
		if slices.Contains(candidate.windows, w) && candidate.when(h) {
			r = candidate
			break
		}
	}
	act, ok := actions[verdict{kind: r.kind, window: w}]
	if !ok {
		return rule{}, 0, fmt.Errorf("rule %q reached a %s verdict in the %s window, which has no action", r.name, r.kind, w)
	}
	return r, act, nil
}
