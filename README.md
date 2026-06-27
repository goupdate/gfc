# GoFunCaller (gfc)

Static call graph generator for Go projects. Produces MermaidJS diagrams and text reports from AST analysis — no runtime instrumentation needed. Designed as a standalone tool and a linter module (`-j` / `--json`).

## Features

- **MermaidJS diagram** — interactive HTML with file subgraphs and function nodes
- **Call graph report** — grouped by file, intra-file calls without prefix
- **Dead code detection** — `-u` / `--unused` finds functions with zero incoming calls
- **JSON output** — `-j` / `--json` for CI/linter integration
- **Reachability filter** — only shows functions reachable from `main()` and `init()`
- **Receiver method resolution** — correctly links `g.method()` to `*CallGraph.method`
- **Zero dependencies** — uses only Go standard library (`go/ast`, `go/parser`)
- **Self-contained HTML** — MermaidJS (v10) embedded into binary, no CDN, no network

## Install

```bash
go install github.com/goupdate/gofuncaller/cmd/gfc@latest
```

## Usage

```
gfc [flags] [directory]
```

### Modes

| Flag | Description |
|---|---|
| _(default)_ | Text report to stdout — calls grouped by file + dead code block |
| `-u`, `--unused` | Dead code list to stdout |
| `-j`, `--json` | JSON to stdout — for CI/linter integration |
| `-g`, `--graphs` | Generate `callgraph.html` + `unused.html` |

### Default mode

```
$ gfc ./pkg
=== main.go ===
main → helper1
main → helper2
helper1 → helper2

=== util.go ===
helper2 → util.go: logError

=== dead code ===
main.go: main.orphanFunc
util.go: main.oldHelper
```

### Dead code detection (`-u`)

```
$ gfc -u ./pkg
dead code:
main.go: main.orphanFunc
util.go: main.oldHelper
```

No output if no dead code. `main()` and `init()` are excluded — entry points, not dead code.

### JSON linter mode (`-j`)

```json
$ gfc -j ./pkg
{
  "calls": {
    "main.go": {
      "main": ["helper1", "helper2"],
      "helper1": ["helper2"]
    }
  },
  "dead": {
    "main.go": ["orphanFunc"]
  }
}
```

### Graph mode (`-g`)

```
$ gfc -g ./pkg
callgraph.html — MermaidJS diagram
unused.html — dead functions diagram
```

## License

MIT