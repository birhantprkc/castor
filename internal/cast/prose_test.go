package cast

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// This file holds the whole repository to two habits about what a comment may CITE, both of
// which failed as conventions first. It lives in this package because internal/cast is the
// composition root, so a check about the tree as a whole is not a check about one package.

// citedLine matches a source citation that carries a line number, with or without its
// directory: "gate.go:37", "core/deliver.go:588". The line number is the whole of the
// offence, which is why the pattern ends in digits.
var citedLine = regexp.MustCompile(`\b[a-z][a-z_]*\.go:[0-9]+\b`)

// citedTest matches a test named in prose. Every one of them is a claim that some property
// is machine-checked somewhere, and a claim of that kind is worth exactly as much as the
// reader's ability to run it.
var citedTest = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*\b`)

// TestNoProductionCommentCitesALineNumber makes the form that rots impossible rather than
// discouraged. Seventeen citations of the shape "core/deliver.go:588" once anchored watch's
// reachability guarantee, so deleting any line above 588 in a 739-line file broke a test in
// another package for a reason no diff reader would connect to the edit they were making.
// The guarantee survives: those comments now name the enclosing function, which moves with
// the code, and watch's own test resolves them that way. Only the digits are banned.
func TestNoProductionCommentCitesALineNumber(t *testing.T) {
	for _, c := range productionComments(t) {
		if cited := citedLine.FindString(c.text); cited != "" {
			t.Errorf("%s cites %q: a line number is stale the moment anything above it moves, so cite the enclosing function instead (file.go:functionName)",
				c.where, cited)
		}
	}
}

// TestEveryCitedTestNamesATestThatExists holds the other half. A comment saying a property is
// pinned by TestSomething is the only evidence most readers will look for that it is pinned
// at all, so a rename that strands the citation is worse than no citation: it reads as
// coverage and answers nothing.
func TestEveryCitedTestNamesATestThatExists(t *testing.T) {
	tests := declaredTests(t)
	for _, c := range productionComments(t) {
		for _, name := range citedTest.FindAllString(c.text, -1) {
			if !tests[name] {
				t.Errorf("%s cites %s, which no test declares: it was renamed or deleted, and the comment now claims a guarantee nobody can run",
					c.where, name)
			}
		}
	}
}

type comment struct {
	where string // path:line, for a human to open
	text  string
}

// productionComments is every comment in every non-test Go file castor ships, read through
// the parser rather than by scanning lines so that a string literal holding something
// citation-shaped (an ffmpeg argument, a fixture playlist) cannot fail either check.
func productionComments(t *testing.T) []comment {
	t.Helper()
	var out []comment
	forEachGoFile(t, func(path string, isTest bool) {
		if isTest {
			return
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, group := range file.Comments {
			for _, line := range group.List {
				pos := fset.Position(line.Pos())
				out = append(out, comment{
					where: filepath.ToSlash(rel(t, path)) + ":" + strconv.Itoa(pos.Line),
					text:  line.Text,
				})
			}
		}
	})
	if len(out) == 0 {
		t.Fatal("no production comments were found at all, so both checks below would pass by walking nothing")
	}
	return out
}

// declaredTests is every Test function the repository declares, which is what a citation has
// to land on.
func declaredTests(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	forEachGoFile(t, func(path string, isTest bool) {
		if !isTest {
			return
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				found[fn.Name.Name] = true
			}
		}
	})
	if len(found) == 0 {
		t.Fatal("no tests were found at all, so every citation would be reported as dangling")
	}
	return found
}

// forEachGoFile walks the three directories castor's own code lives in. third_party is
// excluded because it is vendored: its prose is not castor's to hold to castor's habits.
func forEachGoFile(t *testing.T, visit func(path string, isTest bool)) {
	t.Helper()
	root := repoRoot(t)
	for _, dir := range []string{"cmd", "internal", "e2e"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			visit(path, strings.HasSuffix(path, "_test.go"))
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
}

// repoRoot is derived from this file's own location rather than from a working directory or
// an environment variable, so the walk finds the same tree however the test is invoked.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("the test cannot locate its own source file, so it has no tree to walk")
	}
	return filepath.Join(filepath.Dir(self), "..", "..")
}

func rel(t *testing.T, path string) string {
	t.Helper()
	r, err := filepath.Rel(repoRoot(t), path)
	if err != nil {
		return path
	}
	return r
}
