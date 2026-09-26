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
	Kind   Kind
	Window Window
}

// actions per verdict/window; split is key safety (see TestNoPlayingVerdictRevises).
var actions = map[verdict]action{
	{Kind: Starting, Window: BeforePlay}: keepWatching,
	{Kind: Starting, Window: Opening}:    keepWatching,
	{Kind: Healthy, Window: Playing}:     keepWatching,

	{Kind: Ready, Window: BeforePlay}: open,
	{Kind: Ready, Window: Opening}:    open,

	{Kind: Dead, Window: BeforePlay}:          revise,
	{Kind: Dead, Window: Opening}:             revise,
	{Kind: Stalled, Window: BeforePlay}:       revise,
	{Kind: Undeliverable, Window: BeforePlay}: revise,

	{Kind: Stalled, Window: Playing}:       abandon,
	{Kind: Undeliverable, Window: Playing}: abandon,
	{Kind: Unfetched, Window: Playing}:     abandon,
}

// party: verdict subject; determines whose evidence (not a Kind switch).
type party int

const (
	theProducer party = iota
	theRenderer
)

// rule: health table row (internal; caller sees Fault not row).
type rule struct {
	// Name: row identifier in logs; used for unmatched Health.
	Name string
	// Why: reasoning, carried to caller for user readability.
	Why string
	// Windows: where row applies (action table decides what to do).
	Windows []Window
	// When: health predicate; nil on total row (enables testing without ffmpeg).
	When func(Health) bool
	Kind Kind
	// Blames: whose evidence explains this verdict.
	Blames party
}

// rules: judgment rules (first match, total fallback); add rule+test.
var rules = []rule{{
	// Pre-play read failure is fatal (partial buffer plays then stops).
	Name:    "read-failed",
	Why:     "the source read reached a terminal error before playback could start",
	Windows: []Window{BeforePlay},
	When:    func(h Health) bool { return h.Failed },
	Kind:    Dead,
}, {
	// Producer ended with no output (e.g., container refuses codec).
	Name:    "produced-nothing",
	Why:     "the producer ended without writing anything a renderer could fetch",
	Windows: []Window{Opening},
	When:    func(h Health) bool { return h.Ended && !h.playable() },
	Kind:    Dead,
}, {
	// Producer stopped, renderer exhausted buffer (likely expired playlist).
	Name:    "stalled",
	Why:     "the producer stopped delivering entirely and the renderer has played everything that reached it; the likeliest cause is a signed playlist whose segments have expired (they answer 404), and re-extracting the link is what gets a fresh token",
	Windows: []Window{BeforePlay, Playing},
	When:    func(h Health) bool { return !h.Ended && h.SinceGrowth > StallWindow && !h.buffered() },
	Kind:    Stalled,
}, {
	// Deficit outlasted backoff ceiling: read too slow to catch up.
	Name:    "undeliverable",
	Why:     "the source has delivered fewer media seconds per wall-clock second than playback consumes for longer than a reconnect ceiling, so the cast can never catch up however long it is given",
	Windows: []Window{BeforePlay},
	When:    func(h Health) bool { return h.starving() && h.SinceDeficit > deficitWindow },
	Kind:    Undeliverable,
}, {
	// Deficit not yet outlasted backoff (gate holds, not answered in-flight).
	Name:    "under-playback-rate",
	Why:     "the read is delivering less than playback consumes, and the deficit has not yet outlasted the backoff this read was handed",
	Windows: []Window{BeforePlay},
	When:    Health.starving,
	Kind:    Starting,
}, {
	// In-flight: the deficit outlasted the stall window while a viewer watches.
	Name:    "undeliverable-in-flight",
	Why:     "the source has been delivering less than playback consumes for longer than the stall window, so the renderer's buffer cannot be refilled",
	Windows: []Window{Playing},
	When:    func(h Health) bool { return h.starving() && h.SinceDeficit > StallWindow },
	Kind:    Undeliverable,
}, {
	// A trickle never goes silent long enough to stall, and a paced-down read states no deficit.
	Name:    "starved-in-flight",
	Why:     "the renderer has had nothing left to play for longer than the stall window: what reaches it arrives far slower than it is played",
	Windows: []Window{Playing},
	When:    func(h Health) bool { return !h.Ended && h.starved() > StallWindow },
	Kind:    Undeliverable,
}, {
	// Renderer accepted stream but never fetched bytes.
	Name:    "unfetched",
	Why:     "the renderer accepted the stream URL and was never handed a byte of what this cast produced for it",
	Windows: []Window{Playing},
	When:    func(h Health) bool { return h.Handed == 0 && h.SinceFetch > fetchWindow },
	Kind:    Unfetched,
	Blames:  theRenderer,
}, {
	// Subtitles ready: buffer has media, read delivers, transcription leads.
	Name:    "burn-in-ready",
	Why:     "the buffer holds media, the read has proved it can deliver it, and the transcription is far enough ahead of the encoder",
	Windows: []Window{BeforePlay},
	When: func(h Health) bool {
		return h.Subtitles && (h.playable() && h.measured() && h.leads() || h.Ended)
	},
	Kind: Ready,
}, {
	// Buffer has media, read delivers (or read ended).
	Name:    "ready",
	Why:     "the buffer holds media and the read has proved it can deliver it",
	Windows: []Window{BeforePlay},
	When: func(h Health) bool {
		return !h.Subtitles && (h.playable() && h.measured() || h.Ended)
	},
	Kind: Ready,
}, {
	// Artifact exists or delivery timeout (first byte overdue).
	Name:    "artifact-ready",
	Why:     "the artifact a renderer fetches exists, or this delivery has waited as long as it is willing to",
	Windows: []Window{Opening},
	When:    func(h Health) bool { return h.playable() || h.Overdue },
	Kind:    Ready,
}}

// starting is the total row of both pre-playback windows: nothing has been established.
var starting = rule{
	Name:    "starting",
	Why:     "nothing has been established yet",
	Windows: []Window{BeforePlay, Opening},
	Kind:    Starting,
}

// healthy is the total row of the playing window: a cast in flight with nothing against it.
var healthy = rule{
	Name:    "healthy",
	Why:     "nothing is against this cast",
	Windows: []Window{Playing},
	Kind:    Healthy,
}

func total(w Window) rule {
	if w == Playing {
		return healthy
	}
	return starting
}

func judge(w Window, h Health) (rule, action, error) {
	r := total(w)
	for _, candidate := range rules {
		if slices.Contains(candidate.Windows, w) && candidate.When(h) {
			r = candidate
			break
		}
	}
	act, ok := actions[verdict{Kind: r.Kind, Window: w}]
	if !ok {
		return rule{}, 0, fmt.Errorf("rule %q reached a %s verdict in the %s window, which has no action", r.Name, r.Kind, w)
	}
	return r, act, nil
}
