package pipeline

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// outOfReach is the three rows no unit test can drive, and the reason is wall clock rather
// than wiring: each requires the SAME condition to hold continuously for at least one
// reconnect ceiling (read.BackoffMax), because a judgement reached inside the backoff ffmpeg
// was handed is reporting castor's own impatience. A test that waited that out would take
// minutes.
//
// They are the rows that therefore have to name their production path in prose, checked
// against the wiring by watch's own TestEveryRuleNamesTheProductionMonitorThatReachesIt, and
// the budget below is asserted against that ceiling so that shrinking it turns this exemption
// back into an obligation to drive them.
var outOfReach = []string{"stalled", "undeliverable", "undeliverable-in-flight"}

// citesThisPackage matches a production path naming a monitor THIS package opens, which is
// the scope of what this test can drive at all. A row watched only by the delivery driver
// (the Opening window, over an artifact rather than over a read) is reached where that driver
// lives and not from here, and the check below reads that off the row's own citation rather
// than off a second list somebody has to keep.
var citesThisPackage = regexp.MustCompile(`pipeline/[a-z_]+\.go:[A-Za-z]\w*`)

// budget is how long one watch in this test is given to reach a verdict. Every verdict driven
// here is reached from facts the ports state outright, so the only thing it waits on is the
// polling interval: the longest of them needs the derived number of stated speeds before the
// read may be called short of playback rate, which is that many polls and nothing more.
const budget = 2 * time.Second

// TestEveryRuleAUnitTestCanDriveIsReachedThroughTheRealWiring is the half of the
// reachability property that a table of measurements cannot state, and it is the half a dead
// rule survives.
//
// A rule reachable from a Health somebody wrote down is not a rule castor reaches: the
// facts come from ports, and what production fills them with is narrower than the type. A rule
// keyed on a term the real wiring only ever grows waits for a state that never arrives, and it
// then passes every coupling test, every measurement case, and a first-match search over
// hand-built values, while judging nothing.
//
// So each row below is driven through the production entry points (waitForPlayable and
// supervise) over the real spool and the real read, with the fields production actually
// supplies: the pace is the read's own answer about itself, the buffer is the encoder's
// position as the stream delivery reports it, and the renderer's fetching is whatever the
// mechanism says. Nothing here constructs a watch.Health, and no assertion can be satisfied by
// one.
//
// IT CLOSES OVER ROWS AND NOT OVER VERDICTS, which is the whole of what it is worth. Several
// rows reach the same verdict, so a check that every VERDICT was produced says nothing about
// the rows that produce it: a Playing row keyed on facts the wiring never holds together, and
// reaching a verdict some other row already reaches, passed every part of this file when it
// closed over kinds. The row is read out of the line the watch states it on.
//
// The verdict is read back out of the watch's own statements: the row it names when it
// abandons a cast, when it clears one, and when it says what it is still waiting on. A row
// that keeps the watch running decides nothing and therefore leaves no other trace, which is
// exactly why it is the one that can rot unnoticed.
func TestEveryRuleAUnitTestCanDriveIsReachedThroughTheRealWiring(t *testing.T) {
	stated := captureRules(t)

	// Ready, before playback: bytes in the spool the read is filling, and a read whose pace was
	// withheld, so the gate has nothing left to hold for.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 0)
		if _, err := sp.Write(make([]byte, 4<<20)); err != nil {
			t.Fatal(err)
		}
		return waitForPlayable(ctx, nil, sp, pl)
	})

	// The same gate on a cast that burns subtitles, which is a row of its own and not a
	// variation of the one above: it is the rule that holds the encoder behind the
	// transcription's committed frontier, and it is reached only when a Lead is wired in.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 0)
		if _, err := sp.Write(make([]byte, 4<<20)); err != nil {
			t.Fatal(err)
		}
		return waitForPlayable(ctx, fakeLead{done: true}, sp, pl)
	})

	// Starting, before playback: an empty spool behind a live read. This is the verdict that
	// holds the gate, so the only place it is ever stated is the line the watch reports itself
	// on, and the watch is still holding when the budget runs out.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 0)
		return waitForPlayable(ctx, nil, sp, pl)
	})

	// The other row that holds the gate, and the one the hold exists for: a read allowed to run
	// ahead that is stating speeds under playback rate. It is driven through the reader's own
	// progress port, one fresh sample at a time, because the row only answers once the reader
	// has stated a speed the derived number of times: a single sample is the connection's cost
	// and not the link's rate.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 2.0)
		go starve(ctx, pl)
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

	// Unfetched, in flight: the delivery states that the renderer was handed no byte of what was
	// produced for it, which is the one thing about a renderer this window can say.
	stated.drive(t, func(ctx context.Context) error {
		sp, pl := gateFixture(t, 2.0)
		return supervise(ctx, sp, pl, core.Delivery{
			Consumer: stoppedRenderer{handed: 0, last: time.Now().Add(-watch.StallWindow)},
		})
	})

	for _, row := range watchRows(t) {
		switch {
		case stated.saw(row.name):
		case !citesThisPackage.MatchString(row.doc):
			// Its monitors are opened elsewhere (the delivery driver's artifact gate), so this
			// package cannot drive it and citing it here would be the same untested claim the
			// row-closure exists to stop. It is not unchecked: the citation itself is resolved
			// against the wiring in watch's own reachability test.
		case slices.Contains(outOfReach, row.name):
		default:
			t.Errorf("no cast driven through the real wiring ever reached the %q rule, though it names a monitor this package opens: either the wiring cannot produce the state it answers, in which case the row is dead however satisfiable its predicate looks, or it belongs beside the rows wall clock puts out of reach",
				row.name)
		}
	}

	// The exemption is arithmetic and not a preference, so it is asserted rather than asserted
	// about. Every exempt row needs a condition held continuously for a reconnect ceiling;
	// the day that ceiling fits inside a test, driving them stops being impossible and the
	// prose citation stops being the honest answer.
	if read.BackoffMax <= budget {
		t.Errorf("a reconnect ceiling is now %s, inside this test's %s budget: the rows named out of reach (%v) can be driven through the wiring, and citing a production path for them is no longer the strongest thing available",
			read.BackoffMax, budget, outOfReach)
	}
	if watch.StallWindow <= budget {
		t.Errorf("the stall bound is now %s, inside this test's %s budget: drive the stalled row rather than citing it", watch.StallWindow, budget)
	}
}

// starve feeds the reader's progress port the shape of a link that cannot keep up: a fresh
// sample every poll, each one stating a speed under playback rate. It writes the field the
// real pull's -progress reader writes, under the same lock, so what the watch reads is what a
// reader states about itself and not a Health somebody built.
func starve(ctx context.Context, pl *pull) {
	for position := time.Second; ctx.Err() == nil; position += time.Second {
		pl.mu.Lock()
		pl.progress = media.Progress{Position: position, Bytes: int64(position), Speed: 0.109}
		pl.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// watchRow is one row of the health table as watch's own source declares it: the name the
// watch logs it under, and the comment that names the monitors it is reached through.
type watchRow struct {
	name string
	doc  string
}

// watchRows reads the shipped table out of watch's source rather than taking a list written
// here, because a list written here goes stale exactly when a row is added, which is the
// event this whole file exists to catch. The rows are unexported and must stay that way: what
// is read is their names, which the watch already states in every line it writes.
func watchRows(t *testing.T) []watchRow {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../watch/rules.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("reading the health table: %v", err)
	}

	var rows []watchRow
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Values) != 1 {
				continue
			}
			lit, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			// A row declared on its own, which is how each window's total row is written: it
			// carries a doc comment like any other declaration.
			if name, ok := ruleName(lit); ok {
				rows = append(rows, watchRow{name: name, doc: gen.Doc.Text()})
				continue
			}
			// The ordered table, whose rows are elements of a slice literal and whose comments
			// are therefore attached to nothing and have to be taken by position.
			for _, elt := range lit.Elts {
				row, ok := elt.(*ast.CompositeLit)
				if !ok {
					continue
				}
				name, ok := ruleName(row)
				if !ok {
					continue
				}
				rows = append(rows, watchRow{name: name, doc: commentsWithin(file, row)})
			}
		}
	}

	if len(rows) == 0 {
		t.Fatal("the health table parsed to no rows at all, so this test would pass by finding nothing to check")
	}
	return rows
}

// ruleName reads a row's name, reporting false for a literal that is not a row. A row is
// recognised by carrying both a name and a verdict, which is what every rule has and what
// nothing else in that file does.
func ruleName(lit *ast.CompositeLit) (string, bool) {
	var name string
	var verdict bool
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Name":
			if str, ok := kv.Value.(*ast.BasicLit); ok && str.Kind == token.STRING {
				name, _ = strconv.Unquote(str.Value)
			}
		case "Kind":
			verdict = true
		}
	}
	return name, name != "" && verdict
}

// commentsWithin is everything written inside one row's braces, which is where a row of the
// ordered table carries its reasoning and its citation.
func commentsWithin(file *ast.File, row *ast.CompositeLit) string {
	var doc string
	for _, group := range file.Comments {
		if group.Pos() > row.Lbrace && group.End() < row.Rbrace {
			doc += group.Text()
		}
	}
	return doc
}

// ruleLog is every row the watches under test named about themselves, collected the way
// anything outside the watch learns one: from the lines it states its own decisions on.
//
// Reading the log is not a shortcut around the API, it is the only external statement a rule
// that keeps the watch running ever makes, and it is a statement production makes on every cast
// (see tracker.report). Keying on the name is why watch pins that each row has one of its own.
type ruleLog struct {
	mu   sync.Mutex
	seen map[string]bool
}

// captureRules installs the collector as the default logger for one test, which is where
// the watch writes.
func captureRules(t *testing.T) *ruleLog {
	t.Helper()
	log := &ruleLog{seen: map[string]bool{}}
	restore := slog.Default()
	slog.SetDefault(slog.New(log))
	t.Cleanup(func() { slog.SetDefault(restore) })
	return log
}

// drive runs one production watch until it answers or the budget runs out. What it answered is
// collected off the log, which is where the row that answered is named in all three cases.
func (l *ruleLog) drive(t *testing.T, watching func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	err := watching(ctx)
	var fault *watch.Fault
	switch {
	case err == nil, errors.As(err, &fault), errors.Is(err, context.DeadlineExceeded):
	default:
		t.Fatalf("a watch driven through the production wiring ended with %v, which is neither a verdict nor the budget", err)
	}
}

func (l *ruleLog) record(rule string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[rule] = true
}

func (l *ruleLog) saw(rule string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[rule]
}

func (l *ruleLog) Enabled(context.Context, slog.Level) bool { return true }

func (l *ruleLog) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "rule" {
			l.record(a.Value.String())
		}
		return true
	})
	return nil
}

func (l *ruleLog) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *ruleLog) WithGroup(string) slog.Handler      { return l }
