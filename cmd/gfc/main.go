// golinkgrapher — строит граф вызовов функций Go-проекта и генерирует:
//   1. MermaidJS-диаграмму в HTML (прямоугольники-файлы, скруглённые — функции)
//   2. Текстовый отчёт: caller → callee
//   3. Текстовый отчёт: функция — сколько раз на неё ссылаются
//
// Использование: go run ./cmd/golinkgrapher [директория проекта]
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const helpText = `gfc — Go Function Caller
Static call graph generator for Go projects. Produces MermaidJS diagrams
and text reports from AST analysis — no runtime instrumentation needed.

Usage:
  gfc [flags] [directory]

  If directory is omitted, the current directory (.) is used.

Flags:
  -h, --help      Show this help and exit
  -g, --graphs    Generate callgraph.html + unused.html
  -u, --unused    Print dead code list to stdout
  -j, --json      Print JSON to stdout (for CI/linter integration)

Modes:
  (default)       Text report to stdout — calls grouped by file
                  plus a dead code block at the end.
  -u, --unused    Dead code list to stdout — functions with zero
                  incoming calls. main() and init() are excluded
                  (they are entry points).
  -j, --json      JSON to stdout — structured for CI/linter
                  integration. Output: {"calls": {...}, "dead": [...]}
  -g, --graphs    Generate two HTML files with MermaidJS diagrams:
                    callgraph.html — interactive call graph
                    unused.html   — dead functions diagram
                  MermaidJS is embedded, no CDN or network needed.

Examples:
  gfc ./pkg               Text report for ./pkg
  gfc -u ./pkg             List dead functions only
  gfc -j ./pkg             JSON output for linting pipeline
  gfc -g ./pkg             Generate HTML diagrams
  gfc                      Analyse current directory

Features:
  • MermaidJS diagram — interactive HTML with file subgraphs
    and function nodes
  • Call graph report — grouped by file, intra-file calls
    shown without prefix
  • Full graph — shows ALL functions in the scanned code
  • Dead code detection — finds functions with zero incoming calls
  • External library nodes — functions from packages outside
    the scan directory shown as leaf nodes (no recursion)
  • Receiver method resolution — correctly links g.method()
    to *CallGraph.method
  • Zero external dependencies — uses only Go standard library

License: MIT
`

//go:embed mermaid.min.js
var mermaidJS []byte

// FuncDef — объявление функции.
type FuncDef struct {
	File    string // путь к файлу
	Pkg     string // имя пакета
	Name    string // имя функции
	Recv    string // получатель метода (пусто для функций)
	IsExported bool
}

// CallEdge — ребро вызова.
type CallEdge struct {
	CallerFile string
	CallerFunc string
	Callee     string // как записано в коде: "Func", "pkg.Func", "recv.Method"
}

// CallGraph — полный граф вызовов.
type CallGraph struct {
	Funcs       map[string]*FuncDef // key: полное имя "pkg.Func" | "pkg.Recv.Method"
	Edges       []CallEdge
	filePkg     map[string]string            // file → package name
	fileImports map[string]map[string]string // file → (alias → importPath)
	fset        *token.FileSet
}

func main() {
	var graphsMode, unusedMode, jsonMode, helpMode bool
	root := "."
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		switch a {
		case "-g", "--graphs":
			graphsMode = true
		case "-u", "--unused":
			unusedMode = true
		case "-j", "--json":
			jsonMode = true
		case "-h", "--help":
			helpMode = true
		default:
			root = a
		}
	}

	if helpMode {
		fmt.Print(helpText)
		return
	}

	g := &CallGraph{
		Funcs:       make(map[string]*FuncDef),
		filePkg:     make(map[string]string),
		fileImports: make(map[string]map[string]string),
		fset:        token.NewFileSet(),
	}

	if err := g.parseDir(root); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	g.resolveEdges()

	// JSON-режим — на всём графе, без фильтрации достижимости
	if jsonMode {
		fmt.Print(g.generateJSON())
		return
	}

	// Запоминаем dead-функции ДО фильтрации достижимости
	deadFuncs := g.linterDead()

	// --unused: только потеряшки
	if unusedMode {
		if len(deadFuncs) > 0 {
			fmt.Println("dead code:")
			for _, d := range deadFuncs {
				fmt.Println(d)
			}
		}
		return
	}

	// --graphs: только HTML-файлы
	if graphsMode {
		mermaidHTML := g.generateMermaid(root)
		os.WriteFile("callgraph.html", []byte(mermaidHTML), 0644)
		fmt.Println("callgraph.html — MermaidJS диаграмма")
		unusedHTML := g.generateUnusedHTML(deadFuncs)
		os.WriteFile("unused.html", []byte(unusedHTML), 0644)
		fmt.Println("unused.html — граф потеряшек")
		return
	}

	// default: текстовый отчёт в stdout
	reportCalls := g.generateReportCalls()
	if len(deadFuncs) > 0 {
		reportCalls += "\n=== dead code ===\n"
		for _, d := range deadFuncs {
			reportCalls += d + "\n"
		}
	}
	fmt.Print(reportCalls)
}

// parseDir рекурсивно парсит .go-файлы (кроме _test.go).
func (g *CallGraph) parseDir(root string) error {
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return g.parseFile(path)
	})
	if err != nil {
		return err
	}
	g.resolveEdges()
	return nil
}

// parseFile парсит один .go-файл.
func (g *CallGraph) parseFile(path string) error {
	node, err := parser.ParseFile(g.fset, path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	pkg := node.Name.Name
	g.filePkg[path] = pkg

	// Собираем импорты
	imports := make(map[string]string)
	for _, imp := range node.Imports {
		impPath := strings.Trim(imp.Path.Value, `"`)
		alias := ""
		if imp.Name != nil {
			alias = imp.Name.Name
		} else {
			// Имя по умолчанию — последний элемент пути
			parts := strings.Split(impPath, "/")
			alias = parts[len(parts)-1]
		}
		imports[alias] = impPath
	}
	g.fileImports[path] = imports

	// Собираем объявления функций
	for _, decl := range node.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fd.Name.Name

		// Определяем получатель
		recv := ""
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			recv = typeExprString(fd.Recv.List[0].Type)
		}

		fullName := pkg + "." + name
		if recv != "" {
			fullName = pkg + "." + recv + "." + name
		}

		g.Funcs[fullName] = &FuncDef{
			File:       path,
			Pkg:        pkg,
			Name:       name,
			Recv:       recv,
			IsExported: ast.IsExported(name),
		}
	}

	// Собираем вызовы функций — проходим по AST, запоминая контекст (в какой мы функции)
	var currentFunc string
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			recv := ""
			if x.Recv != nil && len(x.Recv.List) > 0 {
				recv = typeExprString(x.Recv.List[0].Type)
			}
			currentFunc = x.Name.Name
			if recv != "" {
				currentFunc = recv + "." + x.Name.Name
			}
		case *ast.CallExpr:
			if currentFunc == "" {
				break
			}
			callee := callExprString(x)
			if callee == "" || isBuiltin(callee) {
				break
			}
			g.Edges = append(g.Edges, CallEdge{
				CallerFile: path,
				CallerFunc: pkg + "." + currentFunc,
				Callee:     callee,
			})
		}
		return true
	})

	return nil
}

// resolveEdges разрешает короткие имена вызовов в полные, используя импорты.
func (g *CallGraph) resolveEdges() {
	for i, e := range g.Edges {
		resolved := g.resolveCallee(e.CallerFile, e.Callee)
		if resolved != "" {
			g.Edges[i].Callee = resolved
		}
	}
}

// resolveCallee превращает "Func", "pkg.Func" или "var.Method" в полное имя.
func (g *CallGraph) resolveCallee(file, callee string) string {
	// Уже полное имя? (pkg.Func)
	if strings.Contains(callee, ".") {
		parts := strings.SplitN(callee, ".", 2)
		alias := parts[0]
		imports := g.fileImports[file]
		if impPath, ok := imports[alias]; ok {
			impPkg := pkgNameFromPath(impPath)
			return impPkg + "." + parts[1]
		}
		// Не импорт — может быть вызовом метода на локальной переменной:
		//   g.generateMermaid() → ищем в Funcs ключ, оканчивающийся на ".generateMermaid"
		suffix := "." + parts[1]
		for key := range g.Funcs {
			if strings.HasSuffix(key, suffix) {
				return key
			}
		}
		// Не нашли — возвращаем как есть (локальный метод/функция этого же файла)
		return callee
	}
	// Простое имя — ищем в текущем пакете
	pkg := g.filePkg[file]
	if _, ok := g.Funcs[pkg+"."+callee]; ok {
		return pkg + "." + callee
	}
	return callee // не смогли разрешить
}

// linterDead возвращает список функций, которые никто не вызывает.
// Формат: "file.go: pkg.func"
// main и init исключаются (они точки входа).
func (g *CallGraph) linterDead() []string {
	// Считаем входящие вызовы
	incoming := make(map[string]int)
	for _, e := range g.Edges {
		if _, ok := g.Funcs[e.Callee]; ok {
			incoming[e.Callee]++
		}
	}

	var dead []string
	for key, f := range g.Funcs {
		if incoming[key] > 0 {
			continue
		}
		// main и init — точки входа, не dead code
		if f.Recv == "" && (f.Name == "main" || f.Name == "init") {
			continue
		}
		short := filepath.Base(f.File)
		name := f.Name
		if f.Recv != "" {
			name = f.Recv + "." + f.Name
		}
		dead = append(dead, fmt.Sprintf("%s: %s.%s", short, f.Pkg, name))
	}
	sort.Strings(dead)
	return dead
}

// generateUnusedHTML создаёт HTML с графом потеряшек — прямоугольники-файлы,
// внутри которых скруглённые прямоугольники с именами dead-функций.
func (g *CallGraph) generateUnusedHTML(deadFuncs []string) string {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Unused Functions</title>
<script>`)
	sb.Write(mermaidJS)
	sb.WriteString(`</script>
<script>
mermaid.initialize({ startOnLoad: false, maxTextSize: 900000,
  flowchart: { useMaxWidth: true, htmlLabels: true },
  securityLevel: 'loose' });
window.addEventListener('DOMContentLoaded', function() {
  mermaid.run({ querySelector: '.mermaid' }).catch(function(err) {
    document.getElementById('mermaid-error').style.display = 'block';
    document.getElementById('mermaid-error').textContent = 'Render error: ' + err;
  });
});
</script>
<style>
body { font-family: sans-serif; margin: 20px; }
.mermaid { margin: 0 auto; }
h1 { text-align: center; }
#mermaid-error { color: red; display: none; margin: 20px; white-space: pre-wrap; font-family: monospace; }
</style>
</head>
<body>
<h1>Unused Functions</h1>
<div class="mermaid">
flowchart TB
`)

	if len(deadFuncs) == 0 {
		sb.WriteString("  A[\"no lost functions\"]\n")
	} else {
		// Группируем по файлам
		fileFuncs := make(map[string][]string)
		for _, d := range deadFuncs {
			// Формат: "file.go: pkg.func"
			parts := strings.SplitN(d, ":", 2)
			if len(parts) < 2 {
				continue
			}
			file := strings.TrimSpace(parts[0])
			funcName := strings.TrimSpace(parts[1])
			// Для label: убираем pkg. префикс, оставляем func или recv.method
			if idx := strings.IndexByte(funcName, '.'); idx >= 0 {
				funcName = funcName[idx+1:]
			}
			fileFuncs[file] = append(fileFuncs[file], funcName)
		}

		var files []string
		for f := range fileFuncs {
			files = append(files, f)
		}
		sort.Strings(files)

		for _, file := range files {
			funcs := fileFuncs[file]
			subgraphID := sanitizeID("sg_" + file)
			sb.WriteString(fmt.Sprintf("subgraph %s[\"%s\"]\n", subgraphID, file))
			for i, fn := range funcs {
				nodeID := sanitizeID(fmt.Sprintf("%s_%d", file, i))
				sb.WriteString(fmt.Sprintf("  %s(\"%s\")\n", nodeID, fn))
			}
			sb.WriteString("end\n")
		}
	}

	sb.WriteString("</div>\n<div id=\"mermaid-error\"></div>\n</body></html>\n")
	return sb.String()
}

// reachableFromRoots возвращает множество достижимых функций из root-имён (main, init).
func (g *CallGraph) reachableFromRoots(roots []string) map[string]bool {
	reachable := make(map[string]bool)
	queue := []string{}

	// Находим все функции с именами main или init в любом пакете
	for key := range g.Funcs {
		for _, root := range roots {
			if strings.HasSuffix(key, "."+root) {
				reachable[key] = true
				queue = append(queue, key)
			}
		}
	}

	// BFS по рёбрам
	for len(queue) > 0 {
		caller := queue[0]
		queue = queue[1:]
		for _, e := range g.Edges {
			if e.CallerFunc == caller {
				if !reachable[e.Callee] {
					reachable[e.Callee] = true
					queue = append(queue, e.Callee)
				}
			}
		}
	}

	return reachable
}

// filterReachable оставляет только достижимые функции и рёбра.
func (g *CallGraph) filterReachable(reachable map[string]bool) {
	// Фильтруем функции
	for key := range g.Funcs {
		if !reachable[key] {
			delete(g.Funcs, key)
		}
	}
	// Фильтруем рёбра
	filtered := g.Edges[:0]
	for _, e := range g.Edges {
		if reachable[e.CallerFunc] && reachable[e.Callee] {
			filtered = append(filtered, e)
		}
	}
	g.Edges = filtered
}

// externalCallees возвращает уникальные callee, которых нет в g.Funcs
// (функции из библиотек вне каталога сканирования).
func (g *CallGraph) externalCallees() map[string]bool {
	ext := make(map[string]bool)
	for _, e := range g.Edges {
		if _, ok := g.Funcs[e.Callee]; !ok {
			ext[e.Callee] = true
		}
	}
	return ext
}

// generateMermaid создаёт HTML с MermaidJS-диаграммой.
func (g *CallGraph) generateMermaid(root string) string {
	var sb strings.Builder

	extCallees := g.externalCallees()

	sb.WriteString(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Call Graph — ` + filepath.Base(root) + `</title>
<script>`)
	sb.Write(mermaidJS)
	sb.WriteString(`</script>
<script>
mermaid.initialize({ startOnLoad: false, maxTextSize: 900000, maxEdges: 10000,
  flowchart: { useMaxWidth: true, htmlLabels: true, curve: 'basis', rankDir: 'TB' },
  securityLevel: 'loose' });
window.addEventListener('DOMContentLoaded', function() {
  mermaid.run({ querySelector: '.mermaid' }).catch(function(err) {
    document.getElementById('mermaid-error').style.display = 'block';
    document.getElementById('mermaid-error').textContent = 'Render error: ' + err;
  });
});
</script>
<style>
body { font-family: sans-serif; margin: 20px; }
.mermaid { margin: 0 auto; }
h1 { text-align: center; }
#mermaid-error { color: red; display: none; margin: 20px; white-space: pre-wrap; font-family: monospace; }
</style>
</head>
<body>
<h1>Call Graph: ` + filepath.Base(root) + `</h1>
<div class="mermaid">
flowchart TB
`)

	// Группируем функции по файлам
	fileFuncs := make(map[string][]*FuncDef)
	for _, f := range g.Funcs {
		fileFuncs[f.File] = append(fileFuncs[f.File], f)
	}

	// Сортируем файлы
	var files []string
	for f := range fileFuncs {
		files = append(files, f)
	}
	sort.Strings(files)

	funcID := func(f *FuncDef) string {
		short := filepath.Base(f.File)
		return sanitizeID(short + "_" + f.Name)
	}
	extNodeID := func(callee string) string {
		return sanitizeID("ext_" + callee)
	}

	// Рисуем подграфы для каждого файла
	for _, file := range files {
		funcs := fileFuncs[file]
		short := filepath.Base(file)
		subgraphID := sanitizeID("sg_" + short)

		sb.WriteString(fmt.Sprintf("subgraph %s[\"%s\"]\n", subgraphID, short))
		for _, f := range funcs {
			displayName := f.Name
			if f.Recv != "" {
				displayName = f.Recv + "." + f.Name
			}
			id := funcID(f)
			sb.WriteString(fmt.Sprintf("  %s(\"%s\")\n", id, displayName))
		}
		sb.WriteString("end\n")
	}

	// Подграф для внешних (external) функций
	if len(extCallees) > 0 {
		// Группируем по пакетам
		extByPkg := make(map[string][]string) // pkg → [funcName]
		type extPair struct{ pkg, name string }
		var extPairs []extPair
		for callee := range extCallees {
			var pkg, name string
			if idx := strings.IndexByte(callee, '.'); idx >= 0 {
				pkg = callee[:idx]
				name = callee[idx+1:]
			} else {
				pkg = "."
				name = callee
			}
			extByPkg[pkg] = append(extByPkg[pkg], name)
			extPairs = append(extPairs, extPair{pkg, name})
		}

		var pkgs []string
		for p := range extByPkg {
			pkgs = append(pkgs, p)
		}
		sort.Strings(pkgs)

		for _, pkg := range pkgs {
			funcs := extByPkg[pkg]
			sort.Strings(funcs)
			sgID := sanitizeID("ext_pkg_" + pkg)
			sb.WriteString(fmt.Sprintf("subgraph %s[\"%s\"]\n", sgID, pkg+" (ext)"))
			for _, fn := range funcs {
				nid := extNodeID(pkg + "." + fn)
				sb.WriteString(fmt.Sprintf("  %s[\"%s\"]\n", nid, fn))
			}
			sb.WriteString("end\n")
		}
	}

	// Рисуем рёбра
	seen := make(map[string]bool)
	for _, e := range g.Edges {
		callerKey := e.CallerFunc
		src, srcOK := g.Funcs[callerKey]
		if !srcOK {
			continue
		}

		var edgeKey string
		if dst, dstOK := g.Funcs[e.Callee]; dstOK {
			edgeKey = funcID(src) + "->" + funcID(dst)
			if seen[edgeKey] {
				continue
			}
			seen[edgeKey] = true
			sb.WriteString(fmt.Sprintf("%s --> %s\n", funcID(src), funcID(dst)))
		} else if extCallees[e.Callee] {
			edgeKey = funcID(src) + "->" + extNodeID(e.Callee)
			if seen[edgeKey] {
				continue
			}
			seen[edgeKey] = true
			sb.WriteString(fmt.Sprintf("%s --> %s\n", funcID(src), extNodeID(e.Callee)))
		}
	}

	sb.WriteString("</div>\n<div id=\"mermaid-error\"></div>\n</body></html>\n")
	return sb.String()
}

// generateReportCalls — отчёт caller → callee.
// generateJSON возвращает JSON-представление графа.
// Формат: {"calls": {"file.go": {"func": ["callee1", ...]}}, "dead": {"file.go": ["func", ...]}}
func (g *CallGraph) generateJSON() string {
	type fileCalls = map[string]map[string][]string // file → func → callees
	type fileDead = map[string][]string              // file → dead funcs

	calls := make(fileCalls)
	for _, e := range g.Edges {
		src, srcOK := g.Funcs[e.CallerFunc]
		if !srcOK {
			continue
		}
		srcName := src.Name
		if src.Recv != "" {
			srcName = src.Recv + "." + src.Name
		}
		srcFile := filepath.Base(src.File)

		var dstName string
		if dst, dstOK := g.Funcs[e.Callee]; dstOK {
			dstName = dst.Name
			if dst.Recv != "" {
				dstName = dst.Recv + "." + dst.Name
			}
		} else {
			// Внешняя функция — показываем полное имя
			dstName = "[ext] " + e.Callee
		}

		if calls[srcFile] == nil {
			calls[srcFile] = make(map[string][]string)
		}
		calls[srcFile][srcName] = append(calls[srcFile][srcName], dstName)
	}

	dead := make(fileDead)
	for _, d := range g.linterDead() {
		// Формат: "file.go: pkg.func"
		parts := strings.SplitN(d, ":", 2)
		if len(parts) < 2 {
			continue
		}
		file := strings.TrimSpace(parts[0])
		funcName := strings.TrimSpace(parts[1])
		// Убираем pkg.
		if idx := strings.IndexByte(funcName, '.'); idx >= 0 {
			funcName = funcName[idx+1:]
		}
		dead[file] = append(dead[file], funcName)
	}

	out := map[string]interface{}{"calls": calls, "dead": dead}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b) + "\n"
}

func (g *CallGraph) generateReportCalls() string {
	// Группируем рёбра по файлу caller-а
	fileEdges := make(map[string][]CallEdge)
	for _, e := range g.Edges {
		src, srcOK := g.Funcs[e.CallerFunc]
		if !srcOK {
			continue
		}
		fileEdges[src.File] = append(fileEdges[src.File], e)
	}

	// Сортируем файлы
	var files []string
	for f := range fileEdges {
		files = append(files, f)
	}
	sort.Strings(files)

	var sb strings.Builder
	for _, file := range files {
		short := filepath.Base(file)
		sb.WriteString(fmt.Sprintf("=== %s ===\n", short))

		seen := make(map[string]bool)
		for _, e := range fileEdges[file] {
			key := e.CallerFunc + "→" + e.Callee
			if seen[key] {
				continue
			}
			seen[key] = true

			src := g.Funcs[e.CallerFunc]

			srcName := src.Name
			if src.Recv != "" {
				srcName = src.Recv + "." + src.Name
			}

			if dst, dstOK := g.Funcs[e.Callee]; dstOK {
				dstName := dst.Name
				if dst.Recv != "" {
					dstName = dst.Recv + "." + dst.Name
				}
				// Callee из того же файла — без префикса; иначе file.go: func
				if dst.File == file {
					sb.WriteString(fmt.Sprintf("%s → %s\n", srcName, dstName))
				} else {
					dstShort := filepath.Base(dst.File)
					sb.WriteString(fmt.Sprintf("%s → %s: %s\n", srcName, dstShort, dstName))
				}
			} else {
				// Внешняя функция (библиотека вне каталога сканирования)
				sb.WriteString(fmt.Sprintf("%s → [ext] %s\n", srcName, e.Callee))
			}
		}
	}
	return sb.String()
}

// generateReportRefs — отчёт: функция — сколько раз на неё ссылаются.
func (g *CallGraph) generateReportRefs() string {
	refCount := make(map[string]int)

	for _, e := range g.Edges {
		if _, ok := g.Funcs[e.Callee]; ok {
			refCount[e.Callee]++
		}
	}

	type entry struct {
		key   string
		count int
		def   *FuncDef
	}
	var entries []entry
	for _, f := range g.Funcs {
		fullName := f.Pkg + "." + f.Name
		if f.Recv != "" {
			fullName = f.Pkg + "." + f.Recv + "." + f.Name
		}
		entries = append(entries, entry{fullName, refCount[fullName], f})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].key < entries[j].key
	})

	var sb strings.Builder
	sb.WriteString("=== Reference Count (incoming calls) ===\n\n")

	for _, e := range entries {
		short := filepath.Base(e.def.File)
		name := e.def.Name
		if e.def.Recv != "" {
			name = e.def.Recv + "." + e.def.Name
		}
		sb.WriteString(fmt.Sprintf("%s: %s — %d\n", short, name, e.count))
	}
	return sb.String()
}

// --- helpers ---

func typeExprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeExprString(t.X)
	case *ast.SelectorExpr:
		return typeExprString(t.X) + "." + t.Sel.Name
	default:
		return ""
	}
}

func callExprString(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return typeExprString(fun.X) + "." + fun.Sel.Name
	default:
		return ""
	}
}

func isBuiltin(name string) bool {
	switch name {
	case "make", "len", "cap", "new", "append", "copy", "close",
		"delete", "panic", "recover", "print", "println",
		"complex", "real", "imag":
		return true
	}
	return false
}

func pkgNameFromPath(importPath string) string {
	parts := strings.Split(importPath, "/")
	return parts[len(parts)-1]
}

func sanitizeID(s string) string {
	// MermaidJS ID: разрешены буквы, цифры, подчёркивания
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, s)
}