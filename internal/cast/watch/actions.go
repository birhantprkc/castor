package watch

import "fmt"

// action is what a watch does about a verdict.
type action int

const (
	// keepWatching re-reads the facts on the next tick and says what it is waiting on at
	// the reporting cadence.
	keepWatching action = iota
	// open ends the wait successfully: the encoder may read, or the renderer may be
	// pointed at the artifact.
	open
	// revise ends the wait with a fault the attempt loop may still answer by changing
	// the attempt, because no renderer holds a URL yet.
	revise
	// abandon ends the cast with the verdict's reasoning and the measurements behind it.
	abandon
)

// verdict is the pair the action table is keyed on: what was judged, and where.
type verdict struct {
	Kind   Kind
	Window Window
}

// actions is what castor does about each verdict, keyed on the verdict and the window
// it was reached in. The rules are shared between windows; only the action differs, and
// it differs honestly.
//
// The split down the Window axis is the whole safety property of this layer. Every fault
// reached before a renderer holds a URL is revisable, because nothing is watching yet
// and each attempt owns a fresh buffer, so nothing is ever rewound. Every fault reached
// while a renderer is playing abandons with attribution instead, because replaying a
// film from the beginning at minute forty is worse than a clear error. A revision is
// therefore unreachable from the playing window by construction rather than by a check
// somebody has to remember, which is what TestNoPlayingVerdictCanRevise pins.
//
// A verdict missing from this map is an error naming it (see judge), not a silent
// nothing: a rule given a window nobody chose an action for is a cast being observed by
// something with no idea what to do.
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

// Fault is a verdict that ended a watch: what was judged, why, the numbers behind it,
// and whether the attempt that produced it can still be changed.
//
// It is a value and not a formatted string because the attempt loop keys its recovery
// on Kind and its refusal on the measurements, and because the fault has to be able to
// carry the producer's own terminal error: a cast whose reader died must fail WITH that
// error, not with a description of the stage that noticed.
type Fault struct {
	// Kind is the verdict, and Why the row's reasoning.
	Kind Kind
	Why  string

	// Window is where the verdict was reached, and Revise reports that it was reached
	// before a renderer held a URL, so the attempt may still be changed. Revise is set
	// from the action table, so it cannot disagree with it.
	Window Window
	Revise bool

	// Subject names what was being watched, so a fault reads as a statement about the
	// playback gate, the stream output or the playing cast rather than about "a watch".
	Subject string

	// Health is every measurement the verdict was reached on. It is the material a user
	// acts on and the material a recovery chooses from.
	Health Health

	// Err is the producer's own terminal error, when the verdict is about a producer that
	// had one. It is unwrapped, so errors.Is over the cast's result still finds a
	// cancellation or an exit status.
	Err error

	// Evidence is what the producer printed, retained even where its own error path never
	// ran because castor killed it.
	Evidence []string
}

func (f *Fault) Error() string {
	msg := fmt.Sprintf("%s: %s (%s)", f.Subject, f.Why, f.Health)
	if f.Err != nil {
		return msg + ": " + f.Err.Error()
	}
	return msg
}

func (f *Fault) Unwrap() error { return f.Err }
