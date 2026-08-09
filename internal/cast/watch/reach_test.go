package watch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This file holds the reachability property, searched over the states each window can
// actually produce rather than over every Health that can be written down.
//
// A row can be the first match for a hand-built Health and still be unreachable in the
// shipping program, because what production supplies is not the whole space: a rule keyed on a
// fact no monitor in its window fills reads that fact's zero value forever, and one keyed on a
// term that only grows under the real wiring waits for a state that never arrives. Both are
// rows that fail nothing while reading as coverage, and a table of measurements cannot tell
// either of them apart from a healthy row.
//
// So every row names the production monitor it is reached through, as the file and the function
// that opens it, and this test resolves the citation rather than trusting it: the named function
// must really exist in a non-test file and carry a watch.Monitor, that monitor must declare a
// window the row answers, and the row must be able to win there over what those monitors can
// hand the table. A row that cannot answer that is either mis-scoped or dead, and the answer to
// a dead row is to delete it.
//
// The FUNCTION and not the line, because a line number resolves only until an unrelated edit
// above it moves the line, and then this test fails for a reason that is nothing to do with
// reachability and is repaired by re-typing numbers in a third package.
//
// Whether the rows are then really reached by driving that wiring is asserted where the
// wiring lives, over the real spool and the real read (see pipeline's
// TestEveryRuleAUnitTestCanDriveIsReachedThroughTheRealWiring).

// cited matches a production path as a rule's comment states it: a file under internal/cast and
// the function that opens the monitor in it.
var cited = regexp.MustCompile(`([a-z]+/[a-z_]+\.go):([A-Za-z]\w*)`)

// monitorPorts is every field a caller fills a Monitor in through, which is what a citation is
// read for: the facts a row may legitimately read there.
var monitorPorts = []string{"Producer", "Telemetry", "Consumer", "Lead", "Landed", "Headroom", "Delivered", "Grace"}

// suppliers is which port each measurement is read through, straight off tracker.read. A row
// whose predicate depends on a fact no cited monitor supplies is reading a zero value in
// production however satisfiable its predicate looks over a value built by hand.
//
// SinceDeficit needs both, and that is not a detail: the deficit clock only ever starts on a
// reading that found the read starving, which is a judgement about a producer's stated speed
// against a pace, so a site that supplies one without the other supplies neither. Failed needs
// both of its own, for the same kind of reason: the error is read only once the producer's own
// channel says it exists.
var suppliers = map[string][]string{
	"Landed":       {"Landed"},
	"SinceGrowth":  {"Landed"},
	"Position":     {"Telemetry"},
	"Speed":        {"Telemetry"},
	"Samples":      {"Telemetry"},
	"Ended":        {"Producer"},
	"Failed":       {"Producer", "Telemetry"},
	"Headroom":     {"Headroom"},
	"Subtitles":    {"Lead"},
	"Lead":         {"Lead"},
	"LeadDone":     {"Lead"},
	"Handed":       {"Consumer"},
	"SinceFetch":   {"Consumer"},
	"Delivered":    {"Delivered"},
	"SincePlay":    {"Delivered"},
	"Overdue":      {"Grace"},
	"SinceDeficit": {"Telemetry", "Headroom"},
}

// monitor is one production monitor as its call site writes it: the window it declares and the
// ports it fills.
type monitor struct {
	where  string
	window Window
	ports  map[string]bool
}

func TestEveryRuleNamesTheProductionMonitorThatReachesIt(t *testing.T) {
	for _, r := range all() {
		doc, ok := ruleComment(t, r.Name)
		if !ok {
			t.Errorf("rule %q carries no comment, so it names no production path and nothing says how a cast reaches it", r.Name)
			continue
		}
		sites := citedMonitors(t, doc)
		if len(sites) == 0 {
			t.Errorf("rule %q names no production monitor as file.go:function, so nothing distinguishes it from a row only a hand-built Health can reach: cite the wiring that reaches it, or delete the row",
				r.Name)
			continue
		}

		// THE TWO CHECKS BELOW ARE ONE CHECK, PER WINDOW, and that is the whole of what makes
		// either of them worth anything. A row is judged separately in each window it answers,
		// by whichever monitors are opened THERE, so a fact supplied in one window is not
		// supplied in another: unioning the ports over every cited site let a row keyed on a
		// fact only the Opening window fills claim the Playing window as long as it also cited
		// an Opening monitor somewhere in its comment. That row reads a zero value on every
		// playing cast and answers nothing, while reading as a checked citation.
		//
		// So each window the row claims has to be a window some cited monitor is actually
		// opened in (a row claiming a window nobody watches in is judged by nothing), AND the
		// row has to be able to WIN there over the states those monitors can hand the table:
		// every fact filled through a port none of them supplies is held at the zero value the
		// tracker reads for an absent port, and the search then asks whether any state left is
		// one this row answers first.
		//
		// Winning and not merely "reads only facts this window supplies", because those are
		// different questions and only one of them is about a dead row. A row may read an
		// unsupplied fact and be perfectly alive there, if what its absence does is relax the
		// predicate rather than block it: the stall row's buffer term is exactly that before
		// playback, where nobody holds a URL, and the row's own comment says so. What it may
		// not do is REQUIRE a fact the window leaves at zero, which is the shape a table of
		// measurements cannot catch and the shape all three recorded experiments had.
		for _, w := range r.Windows {
			inWindow := slices.DeleteFunc(slices.Clone(sites), func(m monitor) bool { return m.window != w })
			if len(inWindow) == 0 {
				t.Errorf("rule %q answers the %s window and cites no monitor opened in it (%s), so nothing in production ever reaches it there",
					r.Name, w, strings.Join(placesIn(sites), ", "))
				continue
			}
			supplied := map[string]bool{}
			for _, m := range inWindow {
				maps.Copy(supplied, m.ports)
			}
			out := search.of(t, w, absentFrom(t, supplied))
			switch {
			case out.won[r.Name]:
			case out.matched[r.Name]:
				t.Errorf("rule %q never answers a cast in the %s window: every state its monitors there (%s) can produce and it recognises is taken by %q above it, so the pathology it documents is judged by another row",
					r.Name, w, strings.Join(placesIn(inWindow), ", "), out.lost[r.Name])
			default:
				t.Errorf("rule %q recognises no state the monitors it cites for the %s window (%s) can produce: a fact it requires is filled through a port none of them supplies, so in production it reads that fact's zero value forever and judges nothing",
					r.Name, w, strings.Join(placesIn(inWindow), ", "))
			}
		}
	}
}

// absentFrom is the measurements a set of ports leaves at its zero value, which is what turns
// a supply into a state space. A fact this test has no supplier for fails outright rather than
// being assumed present: an unlisted fact would silently be treated as supplied by every
// monitor, which is the assumption that made the union above look sound.
func absentFrom(t *testing.T, supplied map[string]bool) []string {
	t.Helper()
	var absent []string
	for _, f := range facts {
		ports, known := suppliers[f.name]
		if !known {
			t.Fatalf("Health.%s has no supplier named in suppliers, so no window can be told whether it fills it", f.name)
		}
		if !slices.ContainsFunc(ports, func(p string) bool { return !supplied[p] }) {
			continue
		}
		absent = append(absent, f.name)
	}
	return absent
}

// outcome is the first-match result over one supply's states: which rows won, which only ever
// matched, and for those, the row above that took their case. The two are reported apart
// because the fixes are opposite: one is an ordering, the other is a clause.
type outcome struct {
	won     map[string]bool
	matched map[string]bool
	lost    map[string]string
}

// searches memoises the walk per (window, absent facts), because the walk is the expensive
// part and rows watched by the same monitors share it exactly.
type searches map[string]outcome

var search = searches{}

func (s searches) of(t *testing.T, w Window, absent []string) outcome {
	t.Helper()
	key := w.String() + "|" + strings.Join(absent, ",")
	if got, ok := s[key]; ok {
		return got
	}

	out := outcome{won: map[string]bool{}, matched: map[string]bool{}, lost: map[string]string{}}
	for h := range states() {
		if !zeroed(h, absent) {
			continue
		}
		first, _, err := judge(w, h)
		if err != nil {
			t.Fatalf("judge(%s, %s): %v", w, h, err)
		}
		out.won[first.Name] = true
		for _, r := range rules {
			if r.Name == first.Name || !slices.Contains(r.Windows, w) || !r.When(h) {
				continue
			}
			out.matched[r.Name] = true
			// The first winner and not the last, so the row named is the one a reader will
			// find answering the shadowed row's own case rather than whichever state the walk
			// happened to end on.
			if _, ok := out.lost[r.Name]; !ok {
				out.lost[r.Name] = first.Name
			}
		}
	}
	s[key] = out
	return out
}

// zeroed reports that every named measurement is at the value a reading takes when the port
// filling it is absent, which is what a window whose monitors do not supply it can hand the
// table and all it can hand it.
func zeroed(h Health, absent []string) bool {
	v := reflect.ValueOf(h)
	for _, name := range absent {
		if !v.FieldByName(name).IsZero() {
			return false
		}
	}
	return true
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

// ruleComment is one row's own comment, read out of this package's source. It handles the
// two shapes a row is written in: an element of the ordered slice, whose comment is attached
// to no declaration and has to be taken by position (everything written inside the row's
// braces), and a total row, which is a declaration of its own and carries a doc comment.
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
	if doc, ok := declaredRuleComment(file, name); ok {
		return doc, true
	}
	return "", false
}

// declaredRuleComment is the same lookup for a row declared on its own, which is how each
// window's total row is written: held out of the ordered slice so the walk cannot fall off
// the end of it, and documented like any other declaration.
func declaredRuleComment(file *ast.File, name string) (string, bool) {
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
			if !ok || fieldString(lit, "Name") != name {
				continue
			}
			if gen.Doc != nil {
				return gen.Doc.Text(), true
			}
			return "", false
		}
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
// A citation that does not resolve fails the test rather than being skipped: a citation naming a
// function that is gone, or one that no longer opens a watch, is a row whose stated production
// path is fiction, which is worse than no citation at all because it reads as a checked one.
func citedMonitors(t *testing.T, doc string) []monitor {
	t.Helper()
	var found []monitor
	for _, m := range cited.FindAllStringSubmatch(doc, -1) {
		rel, fn := m[1], m[2]
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
		site, ok := monitorAt(t, path, fn)
		if !ok {
			t.Errorf("citation %q names no watch.Monitor: the function is gone or no longer opens one, so the row's stated production path is fiction", m[0])
			continue
		}
		site.where = m[0]
		found = append(found, site)
	}
	return found
}

// monitorAt reads the watch.Monitor one named function opens: the window it declares and the
// ports it fills. The literal may sit inside a closure that function builds, which is where both
// delivery mechanisms write theirs.
//
// The ports are taken from the literal's own keys AND from assignments to a monitor's fields
// anywhere in that file, because a wiring that fills the facts about the read for every window
// at once is the property those windows depend on (see pipeline's watchTheRead, which is what
// stops two windows judging the same read against two different paces). Crediting the file
// rather than the literal is what keeps this test from demanding that each window state those
// facts for itself.
func monitorAt(t *testing.T, path, fn string) (monitor, bool) {
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

	decl := funcNamed(file, fn)
	if decl == nil {
		return monitor{}, false
	}

	var (
		site  monitor
		found bool
	)
	ast.Inspect(decl, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isMonitor(lit.Type) {
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

// funcNamed is the declaration a citation names, or nil where the file has no such function.
func funcNamed(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	return nil
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
