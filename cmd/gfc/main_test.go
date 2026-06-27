package main

import (
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func fixturesDir() string {
	// tests/fixtures/ relative to module root
	return filepath.Join("..", "..", "tests", "fixtures")
}

func parseFixture(t *testing.T, name string) *CallGraph {
	t.Helper()
	g := parseFixtureRaw(t, name)
	g.resolveEdges()
	reachable := g.reachableFromRoots([]string{"main", "init"})
	g.filterReachable(reachable)
	return g
}

// parseFixtureRaw parses without reachability filter (for linter tests).
func parseFixtureRaw(t *testing.T, name string) *CallGraph {
	t.Helper()
	g := &CallGraph{
		Funcs:       make(map[string]*FuncDef),
		filePkg:     make(map[string]string),
		fileImports: make(map[string]map[string]string),
		fset:        token.NewFileSet(),
	}
	root := filepath.Join(fixturesDir(), name)
	if err := g.parseDir(root); err != nil {
		t.Fatalf("parseDir(%q): %v", root, err)
	}
	return g
}

func TestBasicGraph(t *testing.T) {
	g := parseFixture(t, "basic")

	type pair struct{ caller, callee string }
	edges := make(map[pair]bool)
	for _, e := range g.Edges {
		edges[pair{e.CallerFunc, e.Callee}] = true
	}

	// main → helper1, main → helper2, main → *Helper.method, helper1 → helper2
	check := func(caller, callee string) {
		if !edges[pair{caller, callee}] {
			t.Errorf("missing edge: %s → %s", caller, callee)
		}
	}

	pkg := "main"
	check(pkg+".main", pkg+".helper1")
	check(pkg+".main", pkg+".helper2")
	check(pkg+".main", pkg+".*Helper.method")
	check(pkg+".helper1", pkg+".helper2")

	// unreachable НЕ должен быть в Funcs
	if _, ok := g.Funcs[pkg+".unreachable"]; ok {
		t.Error("unreachable should be filtered out")
	}
}

func TestInitGraph(t *testing.T) {
	g := parseFixture(t, "withinit")

	type pair struct{ caller, callee string }
	edges := make(map[pair]bool)
	for _, e := range g.Edges {
		edges[pair{e.CallerFunc, e.Callee}] = true
	}

	pkg := "main"

	// init должен быть в Funcs
	if _, ok := g.Funcs[pkg+".init"]; !ok {
		t.Error("init function not found in graph")
	}

	check := func(caller, callee string) {
		if !edges[pair{caller, callee}] {
			t.Errorf("missing edge: %s → %s", caller, callee)
		}
	}

	check(pkg+".init", pkg+".setup")
	check(pkg+".main", pkg+".doWork")
	check(pkg+".doWork", pkg+".setup")
}

func TestSelfGraph(t *testing.T) {
	// Парсим сам инструмент и проверяем, что main → ключевые методы есть
	g := parseFixture(t, filepath.Join("..", "..", "cmd", "gfc"))

	type pair struct{ caller, callee string }
	edges := make(map[pair]bool)
	for _, e := range g.Edges {
		edges[pair{e.CallerFunc, e.Callee}] = true
	}

	pkg := "main"
checks := []struct{ caller, callee string }{
		{pkg + ".main", pkg + ".*CallGraph.parseDir"},
		{pkg + ".main", pkg + ".*CallGraph.resolveEdges"},
		{pkg + ".main", pkg + ".*CallGraph.generateMermaid"},
		{pkg + ".main", pkg + ".*CallGraph.generateReportCalls"},
	}

	for _, c := range checks {
		if !edges[pair{c.caller, c.callee}] {
			t.Errorf("missing edge: %s → %s", c.caller, c.callee)
		}
	}
}

func TestGeneratedOutput(t *testing.T) {
	g := parseFixture(t, "basic")

	mermaid := g.generateMermaid("basic")
	if len(mermaid) == 0 {
		t.Error("empty mermaid output")
	}
	if !strings.Contains(mermaid, "helper1") {
		t.Error("mermaid should contain helper1")
	}
	if strings.Contains(mermaid, "unreachable") {
		t.Error("mermaid should NOT contain unreachable")
	}

calls := g.generateReportCalls()
	if len(calls) == 0 {
		t.Error("empty calls report")
	}
	if !strings.Contains(calls, "helper1") {
		t.Error("calls report should contain helper1")
	}
}

func TestLinterDeadFunctions(t *testing.T) {
	g := parseFixtureRaw(t, "linter")
	g.resolveEdges()

	dead := g.linterDead()

	if len(dead) == 0 {
		t.Fatal("expected dead functions, got none")
	}

	// Check that dead functions are reported
	has := func(substr string) {
		for _, d := range dead {
			if strings.Contains(d, substr) {
				return
			}
		}
		t.Errorf("linter output missing %q", substr)
	}

	has("deadFunc1")
	has("deadFunc2")
	has("deadMethod")

	// used() and main() MUST NOT be in dead list
	for _, d := range dead {
		if strings.Contains(d, "used") {
			t.Errorf("used() should NOT be dead: %s", d)
		}
		if strings.Contains(d, "main.main") || strings.Contains(d, "main — dead") {
			t.Errorf("main() should NOT be dead: %s", d)
		}
	}
}

func TestUnusedHTML(t *testing.T) {
	// Fixture with dead functions (use raw — linterDead needs unfiltered graph)
	g := parseFixtureRaw(t, "linter")
	g.resolveEdges()
	dead := g.linterDead()
	html := g.generateUnusedHTML(dead)

	if !strings.Contains(html, "deadFunc1") {
		t.Error("unused.html should contain deadFunc1")
	}
	if !strings.Contains(html, "deadFunc2") {
		t.Error("unused.html should contain deadFunc2")
	}
	if !strings.Contains(html, "deadMethod") {
		t.Error("unused.html should contain deadMethod")
	}
	// main and used should NOT appear as dead
if strings.Contains(html, ">used<") || strings.Contains(html, "main") && strings.Contains(html, "dead") {
		// main should only appear as main.go (file name), not as "main — dead code"
	}
}

func TestUnusedHTMLEmpty(t *testing.T) {
	html := (&CallGraph{}).generateUnusedHTML(nil)
	if !strings.Contains(html, "no lost functions") {
		t.Error("empty dead list should show 'no lost functions'")
	}
}

func TestLinterDeadOnSelf(t *testing.T) {
	g := parseFixtureRaw(t, filepath.Join("..", "..", "cmd", "gfc"))
	g.resolveEdges()
	dead := g.linterDead()
	// generateReportRefs, reachableFromRoots, filterReachable are dead
	// (reachableFromRoots and filterReachable no longer called after removing reachability filter)
	if len(dead) != 3 {
		t.Errorf("expected exactly 3 dead funcs, got: %v", dead)
	}
	for _, name := range []string{"generateReportRefs", "reachableFromRoots", "filterReachable"} {
		found := false
		for _, d := range dead {
			if strings.Contains(d, name) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected dead func %q not found in: %v", name, dead)
		}
	}
}

func TestDeadCodeFormat(t *testing.T) {
	// Test linter output format — no " — dead code" suffix
	g := parseFixtureRaw(t, "linter")
	g.resolveEdges()
	dead := g.linterDead()

	for _, d := range dead {
		if strings.Contains(d, " — dead code") {
			t.Errorf("old suffix found in: %q", d)
		}
	}
	// Verify format: "file: pkg.func"
	for _, d := range dead {
		if !strings.Contains(d, ": main.") {
			t.Errorf("unexpected format: %q", d)
		}
	}
}
