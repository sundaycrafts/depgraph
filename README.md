# depgraph

A CLI tool that visualizes source code dependency graphs in your browser using LSP-based static analysis.

> **Docs:** [Architecture (C4 model, API, ports & adapters)](./_docs/architecture.md)

---

## Prerequisites

- Go 1.22+
- Node.js 20+ (LTS)
- [`oapi-codegen`](https://github.com/oapi-codegen/oapi-codegen) — install once:
  ```sh
  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
  ```

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
make dev
```

Open [http://localhost:5173](http://localhost:5173) in your browser.

---

## Common Commands

| Command | Description |
|---|---|
| `make gen` | Regenerate `core/gen/api.gen.go` and `web/src/gen/api.ts` from `api/openapi.yaml` |
| `make build` | Production build — outputs a single Go binary with the web SPA embedded |
| `make dev` | Start Go server (`:8080`) and Vite dev server (`:5173`) in parallel |
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
depgraph <target-dir>
```

Analyzes `<target-dir>` via LSP, starts a local HTTP server, and opens the graph in your browser.
