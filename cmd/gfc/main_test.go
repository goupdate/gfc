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
	g.resolveEdges()
	reachable := g.reachableFromRoots([]string{"main", "init"})
	g.filterReachable(reachable)
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
		{pkg + ".main", pkg + ".*CallGraph.generateReportRefs"},
		{pkg + ".main", pkg + ".*CallGraph.reachableFromRoots"},
		{pkg + ".main", pkg + ".*CallGraph.filterReachable"},
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

	refs := g.generateReportRefs()
	if len(refs) == 0 {
		t.Error("empty refs report")
	}
}