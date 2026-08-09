package watch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This file holds the half of the reachability property that the shadowing search above
// cannot reach, and it is the half that matters.
//
// A row can be the first match for a hand-built Health and still be unreachable in the
// shipping program, because what production supplies is not the whole space: a rule keyed on a
// fact no monitor in its window fills reads that fact's zero value forever, and one keyed on a
// term that only grows under the real wiring waits for a state that never arrives. Both are
// rows that fail nothing while reading as coverage, and a table of measurements cannot tell
// either of them apart from a healthy row.
//
// So every row names the production monitor it is reached through, at file:line, and this test
// resolves the citation rather than trusting it: the cited line must really carry a
// watch.Monitor in a non-test file, it must declare a window the row answers, and it must
// actually supply every fact the row's predicate reads. A row that cannot answer that is
// either mis-scoped or dead, and the answer to a dead row is to delete it.
//
// Whether the verdicts are then really produced by driving that wiring is asserted where the
// wiring lives, over the real spool and the real read (see pipeline's
// TestEveryVerdictAUnitTestCanDriveIsReachedThroughTheRealWiring).

// cited matches a production path as a rule's comment states it: a file under internal/cast
// and the line the monitor's literal is written at.
var cited = regexp.MustCompile(`([a-z]+/[a-z_]+\.go):(\d+)`)

// monitorPorts is every field a caller fills a Monitor in through, which is what a citation is
// read for: the facts a row may legitimately read there.
var monitorPorts = []string{"Producer", "Consumer", "Lead", "Landed", "Headroom", "Delivered", "Grace"}

// suppliers is which port each measurement is read through, straight off tracker.read. A row
// whose predicate depends on a fact no cited monitor supplies is reading a zero value in
// production however satisfiable its predicate looks over a value built by hand.
//
// SinceDeficit needs both, and that is not a detail: the deficit clock only ever starts on a
// reading that found the read starving, which is a judgement about a producer's stated speed
// against a pace, so a site that supplies one without the other supplies neither.
var suppliers = map[string][]string{
	"Landed":       {"Landed"},
	"SinceGrowth":  {"Landed"},
	"Position":     {"Producer"},
	"Speed":        {"Producer"},
	"Samples":      {"Producer"},
	"Ended":        {"Producer"},
	"Failed":       {"Producer"},
	"Headroom":     {"Headroom"},
	"Subtitles":    {"Lead"},
	"Lead":         {"Lead"},
	"LeadDone":     {"Lead"},
	"Handed":       {"Consumer"},
	"SinceFetch":   {"Consumer"},
	"Delivered":    {"Delivered"},
	"SincePlay":    {"Delivered"},
	"Overdue":      {"Grace"},
	"SinceDeficit": {"Producer", "Headroom"},
}

// monitor is one production monitor as its call site writes it: the window it declares and the
// ports it fills.
type monitor struct {
	where  string
	window Window
	ports  map[string]bool
}

func TestEveryRuleNamesTheProductionMonitorThatReachesIt(t *testing.T) {
	for _, r := range rules {
		doc, ok := ruleComment(t, r.Name)
		if !ok {
			t.Errorf("rule %q carries no comment, so it names no production path and nothing says how a cast reaches it", r.Name)
			continue
		}
		sites := citedMonitors(t, doc)
		if len(sites) == 0 {
			t.Errorf("rule %q names no production monitor at file:line, so nothing distinguishes it from a row only a hand-built Health can reach: cite the wiring that reaches it, or delete the row",
				r.Name)
			continue
		}

		// Every window the row claims has to be a window some cited monitor is actually opened
		// in. A row claiming a window nobody watches in is judged by nothing.
		for _, w := range r.Windows {
			if !slices.ContainsFunc(sites, func(m monitor) bool { return m.window == w }) {
				t.Errorf("rule %q answers the %s window and cites no monitor opened in it (%s), so nothing in production ever reaches it there",
					r.Name, w, strings.Join(placesIn(sites), ", "))
			}
		}

		// And every fact the row reads has to be a fact one of those monitors fills. This is
		// what a row keyed on a term production leaves at its zero value looks like from out
		// here, which is the shape a table of measurements cannot catch: the buffer terms are
		// vacuous before playback (no renderer holds a URL, so no delivery reports one), and a
		// pre-playback row keyed on one is waiting for a fact nobody supplies.
		supplied := map[string]bool{}
		for _, m := range sites {
			maps.Copy(supplied, m.ports)
		}
		for _, f := range dependencies(r.When) {
			ports, known := suppliers[f]
			if !known {
				t.Fatalf("rule %q reads %s, which this test has no supplier for: name the port it is read through in suppliers", r.Name, f)
			}
			for _, port := range ports {
				if !supplied[port] {
					t.Errorf("rule %q reads Health.%s, which is filled through Monitor.%s, and no monitor it cites (%s) fills it: in production that fact stays at its zero value, so the row is reachable from a hand-built Health and from nothing else",
						r.Name, f, port, strings.Join(placesIn(sites), ", "))
				}
			}
		}
	}
}

// placesIn names the cited sites for a failure, because the actionable part of one is which
// call site was checked and not only which row failed.
func placesIn(sites []monitor) []string {
	places := make([]string, 0, len(sites))
	for _, m := range sites {
		places = append(places, m.where)
	}
	return places
}

// ruleComment is one row's own comment, read out of this package's source. The rows are
// elements of a slice literal, so their comments are attached to no declaration and have to be
// taken by position: everything written inside the row's braces.
func ruleComment(t *testing.T, name string) (string, bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rules.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing rules.go: %v", err)
	}

	for row := range rowsOf(file, "rules") {
		if fieldString(row, "Name") != name {
			continue
		}
		var doc strings.Builder
		for _, group := range file.Comments {
			if group.Pos() > row.Lbrace && group.End() < row.Rbrace {
				doc.WriteString(group.Text())
			}
		}
		return doc.String(), true
	}
	return "", false
}

// rowsOf yields the element literals of a table declared in this file.
func rowsOf(file *ast.File, table string) func(func(*ast.CompositeLit) bool) {
	return func(yield func(*ast.CompositeLit) bool) {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || value.Names[0].Name != table || len(value.Values) != 1 {
					continue
				}
				lit, ok := value.Values[0].(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, elt := range lit.Elts {
					row, ok := elt.(*ast.CompositeLit)
					if !ok {
						continue
					}
					if !yield(row) {
						return
					}
				}
			}
		}
	}
}

// fieldString reads a string-valued field out of a composite literal.
func fieldString(lit *ast.CompositeLit, field string) string {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != field {
			continue
		}
		if str, ok := kv.Value.(*ast.BasicLit); ok && str.Kind == token.STRING {
			unquoted, err := strconv.Unquote(str.Value)
			if err != nil {
				return ""
			}
			return unquoted
		}
	}
	return ""
}

// citedMonitors resolves every production path a comment names into the monitor written there.
// A citation that does not resolve fails the test rather than being skipped: a stale line
// number is a row whose stated production path is fiction, which is worse than no citation at
// all because it reads as a checked one.
func citedMonitors(t *testing.T, doc string) []monitor {
	t.Helper()
	var found []monitor
	for _, m := range cited.FindAllStringSubmatch(doc, -1) {
		rel, line := m[1], m[2]
		at, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("citation %q: %v", m[0], err)
		}
		// The rules live one directory below the wiring that drives them, so a citation is
		// written the way a reader of this package would follow it.
		path := filepath.Join("..", rel)
		if strings.HasSuffix(rel, "_test.go") {
			t.Errorf("citation %q names a test file: a row's production path is where the shipping program opens the watch, and a test proves nothing about that", m[0])
			continue
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("citation %q does not resolve: %v", m[0], err)
			continue
		}
		site, ok := monitorAt(t, path, at)
		if !ok {
			t.Errorf("citation %q names no watch.Monitor: the line has moved or the wiring is gone, so the row's stated production path is fiction", m[0])
			continue
		}
		site.where = m[0]
		found = append(found, site)
	}
	return found
}

// monitorAt reads the watch.Monitor written at one line of one file: the window it declares
// and the ports it fills.
//
// The ports are taken from the literal's own keys AND from assignments to a monitor's fields
// anywhere in that file, because a wiring that fills the facts about the read for every window
// at once is the property those windows depend on (see pipeline's watchTheRead, which is what
// stops two windows judging the same read against two different paces). Crediting the file
// rather than the literal is what keeps this test from demanding that each window state those
// facts for itself.
func monitorAt(t *testing.T, path string, line int) (monitor, bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	assigned := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if _, ok := sel.X.(*ast.Ident); ok && slices.Contains(monitorPorts, sel.Sel.Name) {
				assigned[sel.Sel.Name] = true
			}
		}
		return true
	})

	var (
		site  monitor
		found bool
	)
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isMonitor(lit.Type) {
			return true
		}
		if fset.Position(lit.Lbrace).Line > line || fset.Position(lit.Rbrace).Line < line {
			return true
		}
		site = monitor{window: windowOf(lit), ports: maps.Clone(assigned)}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && slices.Contains(monitorPorts, key.Name) {
				site.ports[key.Name] = true
			}
		}
		found = true
		return false
	})
	return site, found
}

func isMonitor(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Monitor"
}

// windowOf reads the window a monitor declares. The zero value is BeforePlay, which is what an
// omitted field means at the call site too.
func windowOf(lit *ast.CompositeLit) Window {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Window" {
			continue
		}
		if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
			switch sel.Sel.Name {
			case "Opening":
				return Opening
			case "Playing":
				return Playing
			}
		}
	}
	return BeforePlay
}

// dependencies is which measurements a predicate actually reads, found by flipping one fact at
// a time over the state space and watching whether the answer moves.
//
// It is measured rather than read off the source because that is what makes the supply check
// above hold for the predicate that RUNS: a clause reached through a helper (Health.starving,
// Health.buffered, Health.measured) reads facts the row never names, and those are exactly the
// facts a row can be silently keyed on.
func dependencies(when func(Health) bool) []string {
	depends := map[string]bool{}
	for h := range states() {
		if len(depends) == len(facts) {
			break
		}
		was := when(h)
		for _, f := range facts {
			if depends[f.name] {
				continue
			}
			for _, value := range f.values {
				flipped := h
				value(&flipped)
				if when(flipped) != was {
					depends[f.name] = true
					break
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(depends))
}
