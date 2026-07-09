# depgraph

A CLI tool that visualizes source code dependency graphs in your browser using LSP-based static analysis.

> **Docs:** [Architecture (C4 model, API, ports & adapters)](./_docs/architecture.md)

## Try it out

```sh
curl -L -o depgraph https://github.com/sundaycrafts/depgraph/releases/latest/download/<release_binary>
./depgraph <project root> --exclude=<grob pattern>
# e.g.
# curl -L -o depgraph https://github.com/sundaycrafts/depgraph/releases/latest/download/depgraph-linux-amd64
# depgraph core --exclude=**/*_test.go --exclude=**/main.go --exclude=**/*.gen.go
```

![GUI preview](./_docs/gui_preview.png)

---

## Prerequisites

- Go 1.22+
- Node.js 20+ (LTS)
- [`oapi-codegen`](https://github.com/oapi-codegen/oapi-codegen) — install once:
  ```sh
  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
  ```
- Language server for the language(s) you want to analyze:

  | Language   | Language Server            | Install                                               |
  |------------|----------------------------|-------------------------------------------------------|
  | Go         | gopls                      | `go install golang.org/x/tools/gopls@latest`          |
  | Rust       | rust-analyzer              | `rustup component add rust-analyzer`                  |
  | TypeScript | typescript-language-server | `npm install -g typescript-language-server typescript` |
  | Python     | pyright                    | `npm install -g pyright`                              |

  Language detection is automatic — the target directory is scanned for `go.mod`, `Cargo.toml`, `tsconfig.json`, `pyproject.toml`, `setup.py`, or `requirements.txt`.

---

## Getting Started

```sh
git clone https://github.com/sundaycrafts/depgraph
cd depgraph

# Install web dependencies
cd web && npm install && cd ..

# Generate types from OpenAPI spec (both Go and TypeScript)
make gen

# Start dev servers (Go :8080 + Vite :5173 with proxy)
make dev TARGET_DIR=/path/to/project
```

Open [http://localhost:5173](http://localhost:5173) in your browser.

---

## Common Commands

| Command | Description |
|---|---|
| `make gen` | Regenerate `core/gen/api.gen.go` and `web/src/gen/api.ts` from `api/openapi.yaml` |
| `make build` | Production build — outputs a single Go binary with the web SPA embedded |
| `make dev TARGET_DIR=<path> [DEPGRAPH_ARGS='...']` | Start Go server (`:8080`) and Vite dev server (`:5173`) in parallel |
| `make test` | Run `go test ./...` and `npm test` |

---

## Monorepo Layout

| Directory | Language | Role |
|---|---|---|
| `api/` | YAML | OpenAPI spec — single source of truth for types |
| `core/` | Go | CLI, analysis engine, HTTP server |
| `web/` | TypeScript / React | Browser UI (embedded into the Go binary at build time) |
| `_docs/` | Markdown | Design documents |

---

## Usage

```sh
# Development
make dev TARGET_DIR=/path/to/project

# Production binary
depgraph <target-dir> [--exclude <glob>]...
```

Analyzes `<target-dir>` via LSP, starts a local HTTP server, and opens the graph in your browser.

### Excluding files

`--exclude` accepts [doublestar](https://github.com/bmatcuk/doublestar) glob patterns matched against paths relative to `<target-dir>`. The flag is repeatable. Hidden entries (starting with `.`) are always skipped; everything else must be excluded explicitly.

```sh
# Skip Go test files and the vendor tree
depgraph ./core --exclude='**/*_test.go' --exclude='vendor/**'

# TypeScript: skip node_modules and *.test.ts / *.spec.ts
depgraph ./web --exclude='node_modules/**' --exclude='**/*.test.ts' --exclude='**/*.spec.ts'

# Through the Makefile
make dev TARGET_DIR=$PWD/core DEPGRAPH_ARGS='--exclude=**/*_test.go --exclude=vendor/**'
```

---

## MCP Server Mode

depgraph can run as an [MCP (Model Context Protocol)](https://modelcontextprotocol.io/) stdio server, letting AI assistants like Claude query the dependency graph directly.

```sh
depgraph --mcp
```

The `--mcp` flag skips the HTTP server and browser. depgraph then keeps a long-lived language server alive for every registered project root (called a *project*) and answers tool calls against the live LSP. If the directory you launched depgraph from contains a marker file (`go.mod`, `Cargo.toml`, `tsconfig.json`, `pyproject.toml`, `setup.py`, `requirements.txt`), that directory is registered automatically.

### Tools

| Tool | Arguments | Description |
|---|---|---|
| `add_project` | `root: string`, `excludes?: string[]` | Register a project root and start its language servers. Returns immediately with `{"status":"indexing"}`; subsequent tool calls return a "retry shortly" error until indexing finishes. |
| `find_symbols` | `root: string`, `query: string` | Walk every source file under the project, ask the language server for that file's symbols, and fuzzy-match their names against `query` (case-insensitive subsequence). Returns matching symbols with stateless `id`s usable in `find_references`. |
| `find_references` | `root: string`, `symbol_id: string` | Walk the caller chain upstream from `symbol_id` using `textDocument/references`. Returns every symbol that transitively reaches the target. |

### Claude Code integration

Add to your `~/.claude/settings.json` (or `.claude/settings.json` for a project-scoped config):

```json
{
  "mcpServers": {
    "depgraph": {
      "command": "/path/to/depgraph",
      "args": ["--mcp"],
      "cwd": "<target-dir>"
    }
  }
}
```

Then in Claude Code you can ask things like:

> "Which functions transitively call `Analyze`?"

Claude will call `find_symbols` to look up the ID, then `find_references` to walk the caller chain.

---

## Roadmap

- **MCP-standard progress notifications** for project readiness (replacing the previous custom `notifications/claude/channel` events).
- **Recursive project discovery** at session start: walk the workspace respecting `.gitignore` and auto-register every nested marker (`go.mod`, `Cargo.toml`, `tsconfig.json`, `pyproject.toml`, `package.json`, …).
- **User-overridable LSP config**: layered on top of the built-in zed-style config (global and project-local).

---

## Releasing

1. Merge all changes to `main` and verify CI passes.
2. Tag the commit and push:
   ```sh
   git tag v1.2.3
   git push origin v1.2.3
   ```
3. GitHub Actions builds binaries for Linux and macOS (amd64 + arm64) and publishes a GitHub Release automatically.
