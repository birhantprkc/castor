package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/watch"
)

// verdicts is every verdict castor reaches about a cast, in the order the constants are
// declared. Each one below is either DRIVEN through the production wiring by the test in this
// file or named as out of reach of any unit test, with the arithmetic that says so.
//
// The list is closed by an arithmetic guard rather than by trust (see the end of the test): the
// value one past the last entry must still be unnamed, so a verdict added to watch without
// being driven here fails rather than passing unnoticed.
var verdicts = []watch.Kind{
	watch.Starting,
	watch.Ready,
	watch.Healthy,
	watch.Stalled,
	watch.Undeliverable,
	watch.Dead,
	watch.Unfetched,
}

// outOfReach is the two verdicts no unit test can drive, and the reason is wall clock rather
// than wiring: both require the SAME condition to hold continuously for at least one reconnect
// ceiling (read.BackoffMax), because a judgement reached inside the backoff ffmpeg was handed
// is reporting castor's own impatience. A test that waited that out would take minutes.
//
// They are the rows that therefore have to name their production path in prose, checked
// against the wiring by watch's own TestEveryRuleNamesTheProductionMonitorThatReachesIt, and
// the budget below is asserted against that ceiling so that shrinking it turns this exemption
// back into an obligation to drive them.
var outOfReach = []watch.Kind{watch.Stalled, watch.Undeliverable}

// budget is how long one watch in this test is given to reach a verdict. It is a few polling
// intervals: every verdict driven here is reached from facts the ports state outright, so a
// watch that has not answered inside it is not slow, it is waiting on wall clock.
const budget = time.Second

// TestEveryVerdictAUnitTestCanDriveIsReachedThroughTheRealWiring is the half of the
// reachability property that a table of measurements cannot state, and it is the half a dead
// rule survives.
//
// A verdict reachable from a Health somebody wrote down is not a verdict castor reaches: the
// facts come from ports, and what production fills them with is narrower than the type. A rule
// keyed on a term the real wiring only ever grows waits for a state that never arrives, and it
// then passes every coupling test, every measurement case, and a first-match search over
// hand-built values, while judging nothing.
//
// So each verdict below is driven through the production entry points (waitForPlayable and
// supervise) over the real spool and the real read, with the fields production actually
// supplies: the pace is the read's own answer about itself, the buffer is the encoder's
// position as the stream delivery reports it, and the renderer's fetching is whatever the sink
// says. Nothing here constructs a watch.Health, and no assertion can be satisfied by one.
//
// The verdict is read back out of the watch's own statements: the fault it returns, the nil it
// opens with, and the log line it says what it is waiting on in. A verdict that keeps the watch
// running decides nothing and therefore leaves no other trace, which is exactly why it is the
// one that can rot unnoticed.
func TestEveryVerdictAUnitTestCanDriveIsReachedThroughTheRealWiring(t *testing.T) {
	stated := captureVerdicts(t)

	// Ready, before playback: bytes in the spool the read is filling, and a read whose pace was
	// withheld, so the gate has nothing left to hold for.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 0)
		if _, err := sp.Write(make([]byte, 4<<20)); err != nil {
			t.Fatal(err)
		}
		return waitForPlayable(ctx, nil, sp, pl)
	})

	// Starting, before playback: an empty spool behind a live read. This is the verdict that
	// holds the gate, so the only place it is ever stated is the line the watch reports itself
	// on, and the watch is still holding when the budget runs out.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 0)
		return waitForPlayable(ctx, nil, sp, pl)
	})

	// Dead, before playback: the read reached a terminal error, which it reports through the
	// same two ports production uses (its Done channel and its own error).
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 2.0)
		pl.err = errors.New("upstream pull: exit status 1")
		close(pl.done)
		return waitForPlayable(ctx, nil, sp, pl)
	})

	// Healthy, in flight: a renderer that is fetching, over the buffer the stream delivery
	// reports. Both facts are the delivery's, forwarded whole by supervise.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 2.0)
		return supervise(ctx, sp, pl, core.Delivery{
			Consumer:  stoppedRenderer{handed: 8 << 20, last: time.Now()},
			Delivered: func() time.Duration { return 20 * time.Minute },
		})
	})

	// Unfetched, in flight: the sink states that the renderer was handed no byte of what was
	// produced for it, which is the one thing about a renderer this window can say.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 2.0)
		return supervise(ctx, sp, pl, core.Delivery{
			Consumer: stoppedRenderer{handed: 0, last: time.Now().Add(-watch.StallWindow)},
		})
	})

	for _, k := range verdicts {
		switch {
		case stated.saw(k):
		case slices.Contains(outOfReach, k):
		default:
			t.Errorf("no cast driven through the real wiring ever reached the %s verdict: either the wiring cannot produce it, in which case the rule that reaches it is dead however satisfiable its predicate looks, or it belongs beside the verdicts wall clock puts out of reach", k)
		}
	}

	// The exemption is arithmetic and not a preference, so it is asserted rather than asserted
	// about. Both exempt verdicts need a condition held continuously for a reconnect ceiling;
	// the day that ceiling fits inside a test, driving them stops being impossible and the
	// prose citation stops being the honest answer.
	if read.BackoffMax <= budget {
		t.Errorf("a reconnect ceiling is now %s, inside this test's %s budget: the verdicts named out of reach (%v) can be driven through the wiring, and citing a production path for them is no longer the strongest thing available",
			read.BackoffMax, budget, outOfReach)
	}
	if watch.StallWindow <= budget {
		t.Errorf("the stall bound is now %s, inside this test's %s budget: drive the stalled verdict rather than citing it", watch.StallWindow, budget)
	}

	// And the list above is closed: the value one past the last verdict must still have no name
	// of its own, or watch reaches a verdict nothing here drives or exempts.
	if next := watch.Kind(len(verdicts)); next.String() != watch.Starting.String() {
		t.Errorf("watch reaches a %q verdict that is neither driven through the wiring here nor named as out of reach", next)
	}
}

// verdictLog is every verdict the watches under test stated about themselves, collected the way
// anything outside the watch learns one: from what it returns, and from the line it reports
// itself on.
//
// Reading the log is not a shortcut around the API, it is the only external statement a verdict
// that keeps the watch running ever makes, and it is a statement production makes on every cast
// (see tracker.report). Keying on the name is why watch pins that each verdict has one of its
// own.
type verdictLog struct {
	mu   sync.Mutex
	seen map[string]bool
}

// captureVerdicts installs the collector as the default logger for one test, which is where
// the watch writes.
func captureVerdicts(t *testing.T) *verdictLog {
	t.Helper()
	log := &verdictLog{seen: map[string]bool{}}
	restore := slog.Default()
	slog.SetDefault(slog.New(log))
	t.Cleanup(func() { slog.SetDefault(restore) })
	return log
}

// drive runs one production watch until it answers or the budget runs out, and records what it
// answered.
func (l *verdictLog) drive(t *testing.T, watching func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	err := watching(ctx)
	var fault *watch.Fault
	switch {
	case err == nil:
		// The watch opened, and open is the action of exactly one verdict (see watch.actions).
		l.record(watch.Ready.String())
	case errors.As(err, &fault):
		l.record(fault.Kind.String())
	case errors.Is(err, context.DeadlineExceeded):
		// Still holding when the budget ran out, which is the whole of what a verdict that keeps
		// watching does. What it was holding on is in the log.
	default:
		t.Fatalf("a watch driven through the production wiring ended with %v, which is neither a verdict nor the budget", err)
	}
}

func (l *verdictLog) record(kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[kind] = true
}

func (l *verdictLog) saw(k watch.Kind) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[k.String()]
}

func (l *verdictLog) Enabled(context.Context, slog.Level) bool { return true }

func (l *verdictLog) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "verdict" {
			l.record(a.Value.String())
		}
		return true
	})
	return nil
}

func (l *verdictLog) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *verdictLog) WithGroup(string) slog.Handler      { return l }
