package pipeline

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/carriage"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/deliver/spool"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// fakeLead stands in for the transcriber so the gate's wiring can be exercised without a
// whisper model and a cgo build, which is the whole reason the lead port is declared at its
// consumer.
type fakeLead struct {
	latest float64
	done   bool
}

func (f fakeLead) LatestEnd() float64 { return f.latest }
func (f fakeLead) Done() bool         { return f.done }

func TestWaitForPlayableHoldsUntilTheTranscriptionLeads(t *testing.T) {
	// A read granted no pace, so the deliverability question is not asked here: this test
	// is about the transcription frontier and nothing else, and a starving-read verdict
	// would hold the gate for a reason the assertion is not making.
	sp, pl := gateFixture(t, 0)
	if _, err := sp.Write(make([]byte, 4<<20)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	opened := make(chan error, 1)
	go func() {
		opened <- waitForPlayable(ctx, fakeLead{latest: 1}, sp, pl)
	}()

	select {
	case err := <-opened:
		t.Fatalf("the gate opened ahead of the transcription's committed frontier (err %v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	// Cancelling is how the gate ends when its condition is never met, and the cause is
	// what the caller reports.
	cancel()
	if err := <-opened; !errors.Is(err, context.Canceled) {
		t.Errorf("gate error = %v, want context.Canceled", err)
	}
}

// TestTheGateHoldsAStarvingReadRatherThanRefusingIt is the observed failure through the
// real gate, and it pins the two things a deficit measured before playback must do.
//
// It must not OPEN. The run this exists for landed 33 KB and one second of media in thirty
// seconds against a reader allowed twice realtime, opened the gate on those 33 KB, and
// handed a renderer a stream it read bytes_sent=0 from. That is also what pins the wiring:
// a speed means nothing without the pace it is measured against, so a gate given no pace
// opens on the same 33 KB.
//
// It must not REFUSE on this evidence either. The deficit has run for a couple of seconds
// here, well inside the reconnect ceiling ffmpeg was handed to wait out a 429, and a
// refusal walks and burns every other admitted link (single-use signed URLs among them)
// for a fault that need not be about any of them. The conviction that eventually arrives
// once the deficit outlasts that ceiling is a judgement over measurements, pinned in
// watch's own tests, because sixty seconds of continuous deficit is not something a test
// can wait out.
func TestTheGateHoldsAStarvingReadRatherThanRefusingIt(t *testing.T) {
	sp, pl := gateFixture(t, 2.0)
	if _, err := sp.Write(make([]byte, 33088)); err != nil {
		t.Fatal(err)
	}
	feedTheObservedRun(t, pl)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	settled := make(chan error, 1)
	go func() { settled <- waitForPlayable(ctx, nil, sp, pl) }()

	select {
	case err := <-settled:
		if err == nil {
			t.Fatal("the gate opened on a read delivering a fraction of realtime, which is a renderer pointed at a stream castor has measured as unwatchable")
		}
		t.Fatalf("the gate refused a cast inside the backoff ceiling its own read policy handed the reader: %v", err)
	case <-time.After(2 * time.Second):
	}

	cancel()
	if err := <-settled; !errors.Is(err, context.Canceled) {
		t.Errorf("gate error = %v, want context.Canceled: the hold ends with the cast, not with a verdict", err)
	}
}

// TestAReadCastorItselfCanSlowDownIsNotJudgedAsALink is the other half of the same
// misattribution. Speed measures the whole reader's throughput while the pace was only ever
// an allowance on the network, so two things castor does inside the read make the
// comparison meaningless, and both of them read like a starving source:
//
//   - a PCM tee, whose consumer loads a whisper model and runs inference on the goroutine
//     draining the pipe, which throttles the whole download while it does;
//   - a floor encode, which is castor's own libx264 being measured.
//
// Either one refused the cast on the observed run's numbers and then burned every admitted
// link looking for a better one. So the pace is withheld, the deliverability question goes
// unasked, and the same evidence opens the gate.
func TestAReadCastorItselfCanSlowDownIsNotJudgedAsALink(t *testing.T) {
	for _, tt := range []struct {
		name string
		load func(*pull)
	}{{
		name: "a read teeing PCM to a transcription",
		load: func(p *pull) { _, p.pcmOut = io.Pipe() },
	}, {
		name: "a read producing a track rather than copying it",
		load: func(p *pull) { p.reencode = carriage.Axes{Video: true} },
	}} {
		t.Run(tt.name, func(t *testing.T) {
			sp, pl := gateFixture(t, 2.0)
			tt.load(pl)
			if _, err := sp.Write(make([]byte, 33088)); err != nil {
				t.Fatal(err)
			}
			feedTheObservedRun(t, pl)

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := waitForPlayable(ctx, nil, sp, pl); err != nil {
				t.Errorf("the gate judged a read castor itself was slowing down as a starving link: %v", err)
			}
		})
	}
}

// TestSuperviseLeavesAPausedViewerTheCastTheyArePausing is the pause through the real
// supervisor, on the two readings a quiet renderer produces, and neither of them ends the
// cast.
//
// A paused renderer stops requesting segments, or stops draining the socket, and the sink
// reports that as silence. Ending the cast on it tears down the encoder and closes the server
// two and a half minutes into somebody standing up, and nothing the supervisor can read tells
// that from a renderer that went away: the buffer figure is the ENCODER's position, so on this
// leg it keeps growing through the silence, and a verdict that waited for it to look empty was
// waiting on a number the renderer has no part in. What the renderer actually took is settled
// once the cast has ended (see core.Undelivered), where it is arithmetic.
//
// The stall window is not waited out: the sink states when it last handed over bytes, which is
// the fact production reads.
func TestSuperviseLeavesAPausedViewerTheCastTheyArePausing(t *testing.T) {
	// No pace, so the deliverability arm asks nothing here: this test is about the renderer's
	// silence and what is made of it.
	const noPace = 0
	stopped := stoppedRenderer{handed: 8 << 20, last: time.Now().Add(-watch.StallWindow - time.Second)}

	// One row, because a delivery cannot hand over half of what it is judged on: the two facts
	// travel as one value built by one constructor (see core.Delivery). The row that used to sit
	// beside this one supervised a delivery reporting a renderer with no buffer behind it, which
	// core has no way to produce.
	sp, pl := gateFixture(t, noPace)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	err := supervise(ctx, sp, pl, core.Delivery{
		Consumer:  stopped,
		Delivered: func() time.Duration { return 20 * time.Minute },
	})
	var fault *watch.Fault
	if errors.As(err, &fault) {
		t.Fatalf("the cast a viewer paused was ended as %s: %v", fault.Kind, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("supervise = %v, want the cast to keep running while its viewer is paused", err)
	}
}

// TestBothWindowsJudgeTheSameReadAgainstTheSamePace is the misattribution the wiring exists
// to make unreachable, and it is a property of the two windows rather than of either.
//
// The pace is what arms the deliverability arm (watch.Health.Headroom), and it is withheld
// from a read castor itself throttles or encodes, because those measure castor's own work and
// not the link. Taking it from the read in one window and from the attempt's policy in the
// other left the same read unjudged before playback and judged against 2x after it, which is
// the worse half: at the observed 0.0627x that abandons a whisper cast or a floor encode
// mid-title, to a viewer who is watching, naming a source link that was never the bottleneck,
// and nothing starts that cast over.
//
// Each window is driven to a verdict by a route of its own that this test is not about (a read
// that reached a terminal error before playback, a renderer that accepted the URL and never
// came for the bytes), purely because a Fault carries the measurements it was reached on, and
// the pace is one of them. It cannot be asserted through the deliverability verdict itself:
// that arm acts only on a deficit sustained past two reconnect ceilings, which is two and a
// half minutes no test waits out, and it is pinned over measurements in watch's own table.
func TestBothWindowsJudgeTheSameReadAgainstTheSamePace(t *testing.T) {
	// What the read policy granted, and what the attempt therefore carries: the figure a
	// window that asked the policy instead of the read would judge every shape below against.
	const granted = 2.0

	for _, tt := range []struct {
		name string
		load func(*pull)
		want float64
	}{{
		name: "a read copying both tracks off the link",
		load: func(*pull) {},
		want: granted,
	}, {
		name: "a read teeing PCM to a transcription",
		load: func(p *pull) { _, p.pcmOut = io.Pipe() },
		want: 0,
	}, {
		name: "a read producing a track rather than copying it",
		load: func(p *pull) { p.reencode = carriage.Axes{Video: true} },
		want: 0,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			sp, pl := gateFixture(t, granted)
			tt.load(pl)

			// A read that died is the fastest verdict the pre-playback window reaches, and it
			// reaches it whatever is in the buffer.
			pl.err = errors.New("upstream pull: exit status 1")
			close(pl.done)
			gate := paceBehindTheVerdict(t, func(ctx context.Context) error {
				return waitForPlayable(ctx, nil, sp, pl)
			})

			// And a renderer that has never come for the bytes is the fastest verdict the
			// in-flight window reaches. The silence is stated by the sink rather than waited out.
			never := stoppedRenderer{handed: 0, last: time.Now().Add(-watch.StallWindow - time.Second)}
			inFlight := paceBehindTheVerdict(t, func(ctx context.Context) error {
				return supervise(ctx, sp, pl, core.Delivery{Consumer: never})
			})

			if gate != inFlight {
				t.Errorf("the gate judged this read against %gx and the supervisor against %gx: the same read is measured against two different paces, and the one that disagrees with the read ends a cast someone is watching", gate, inFlight)
			}
			if gate != tt.want {
				t.Errorf("the pace this read is judged against = %gx, want %gx", gate, tt.want)
			}
		})
	}
}

// TestNoWindowCanStateItsOwnPaceForARead is the mechanism behind the test above, pinned on its
// own so that the agreement is a property of the wiring rather than of two literals that happen
// to match today. A window says which side of the gate it is on and what the renderer's side
// can report; the read's own answer overwrites anything it says about the read, so the way the
// two windows drifted apart in the first place (each stating a pace of its own) cannot be
// written down again.
func TestNoWindowCanStateItsOwnPaceForARead(t *testing.T) {
	sp, pl := gateFixture(t, 2.0)
	_, pl.pcmOut = io.Pipe() // a read castor throttles: its pace is withheld

	pace := paceBehindTheVerdict(t, func(ctx context.Context) error {
		return watchTheRead(ctx, sp, pl, watch.Monitor{
			Subject:  "a window with an opinion about the read",
			Window:   watch.Playing,
			Headroom: 8,
			Consumer: stoppedRenderer{handed: 0, last: time.Now().Add(-watch.StallWindow - time.Second)},
		})
	})
	if pace != pl.judgedPace() {
		t.Errorf("the read was judged against %gx rather than the %gx it answers for itself, so a window can still state a pace of its own", pace, pl.judgedPace())
	}
}

// TestNothingButAReadsOwnAnswerEverSuppliesAPace is the precondition for holding a cast to a
// height ceiling at all, pinned structurally because the tests above can only pin the sites
// that exist today.
//
// The ceiling binds every leg that PRODUCES bytes, so it buys re-encodes on legs that used to
// copy: a 2160p source under a 1080 cap now decodes, scales and encodes, which needs hardware
// to hold realtime and sits far under 1.0x on a software-only host. A watch handed a pace over
// work of that shape convicts the ORIGIN for it, and conviction is not a local mistake: before
// playback it walks and burns every remaining candidate, re-touching single-use signed URLs for
// a deficit none of them caused, and in flight it ends a cast someone is watching with a
// message sending them after their network.
//
// So the pace has exactly one supplier in the whole cast path, and it is the read answering
// about itself (pull.judgedPace, which withholds it from a read castor throttles or produces).
// The legs the ceiling newly binds supply none at all: a remux's encode is watched only while
// its artifact is opening, where no rule reads a pace, and a delivery that filled one in would
// be measuring castor's own encoder against an allowance granted to a network.
//
// It is read off the source rather than driven, because what has to be impossible is a SITE:
// a supplier added tomorrow is a cast abandoned for work castor chose to do, and no test over
// the wiring that exists can fail for one that does not exist yet.
func TestNothingButAReadsOwnAnswerEverSuppliesAPace(t *testing.T) {
	const answer = "pl.judgedPace()"

	suppliers := map[string]string{}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			// The watch package itself is where the port and the facts it fills are declared, so
			// its own reads of the term are the mechanism rather than a supplier of it.
			if d.Name() == "watch" {
				return fs.SkipDir
			}
			return nil
		case !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			for expr, value := range paceSuppliedBy(n) {
				var text strings.Builder
				if err := printer.Fprint(&text, fset, value); err != nil {
					t.Fatal(err)
				}
				suppliers[fmt.Sprintf("%s: %s", fset.Position(expr.Pos()).String(), text.String())] = text.String()
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(suppliers) == 0 {
		t.Fatalf("nothing in the cast path supplies a pace, so no deliverability verdict can be reached at all and this test is measuring the wrong thing")
	}
	for where, value := range suppliers {
		if value != answer {
			t.Errorf("%s supplies the pace a cast is judged against as %s rather than %s: only the read can answer for itself, and a site that answers for it judges castor's own encode as a starving link",
				where, value, answer)
		}
	}
}

// paceSuppliedBy yields every expression one node supplies a watch pace through, with the site
// it is written at: a Headroom key on a Monitor literal, or an assignment to one's field.
func paceSuppliedBy(n ast.Node) func(func(ast.Node, ast.Expr) bool) {
	return func(yield func(ast.Node, ast.Expr) bool) {
		switch node := n.(type) {
		case *ast.CompositeLit:
			sel, ok := node.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Monitor" {
				return
			}
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Headroom" && !yield(kv, kv.Value) {
					return
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Headroom" || i >= len(node.Rhs) {
					continue
				}
				if !yield(sel, node.Rhs[i]) {
					return
				}
			}
		}
	}
}

// paceBehindTheVerdict runs one watch to its verdict and reports the pace the read was judged
// against, which every fault carries among the measurements it was reached on.
func paceBehindTheVerdict(t *testing.T, watching func(context.Context) error) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var fault *watch.Fault
	if err := watching(ctx); !errors.As(err, &fault) {
		t.Fatalf("the watch ended with %v rather than a verdict carrying the numbers it was reached on", err)
	}
	return fault.Health.Headroom
}

// stoppedRenderer is what a sink can report about a renderer that is not fetching now: how much
// of the program it was ever handed, and when a byte of it last moved. A figure that went quiet
// is what both a viewer who paused and a renderer that went away look like from out here; zero
// is a renderer that was handed no byte of what castor made for it, whether it asked or not.
type stoppedRenderer struct {
	handed int64
	last   time.Time
}

func (s stoppedRenderer) Handed() (int64, time.Time) { return s.handed, s.last }

// feedTheObservedRun replays the reader's own account of itself, one block at a time,
// exactly as the observed run reported it. Blocks are spaced past the gate's polling
// interval so each is a sample it really saw.
func feedTheObservedRun(t *testing.T, pl *pull) {
	t.Helper()
	go func() {
		for _, speed := range []media.Speed{0.39, 0.159, 0.109, 0.0627, 0.05, 0.04, 0.03} {
			pl.mu.Lock()
			pl.progress = media.Progress{Position: time.Second, Bytes: 33088, Speed: speed}
			pl.mu.Unlock()
			time.Sleep(250 * time.Millisecond)
		}
	}()
}

// gateFixture is a spool and a pull with no ffmpeg behind it: the gate reads the
// download's state through the pull's channels, its progress sample and the spool's size,
// so those are all it needs to run.
//
// pace is what the read policy granted this download, which is the only thing that makes a
// stated speed mean anything (see pull.judgedPace).
func gateFixture(t *testing.T, pace float64) (*spool.Spool, *pull) {
	t.Helper()
	sp, err := spool.New(filepath.Join(t.TempDir(), "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.CloseWrite(nil) })

	pl := &pull{
		spool:  sp,
		done:   make(chan struct{}),
		source: ffmpeg.NetworkSource{Read: read.Policy{Pace: read.Pace{Realtime: pace}}},
	}
	return sp, pl
}
