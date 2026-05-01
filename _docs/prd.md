# PRD: Code Dependency Graph Explorer

## 1. Overview

This product visualizes source code dependencies (files and symbols) and enables interactive exploration.
In the MVP, a dependency graph is generated via Language Server Protocol (LSP)-based static analysis and rendered in a Web UI.

Users specify a target directory from the CLI; the resulting graph is viewable and navigable in the browser.
Clicking a node on the graph opens an editor view with the relevant code range highlighted.

A ports-and-adapters architecture is adopted to allow future IDE plugin integration.

## 2. Goals

- Visualize dependency graphs across languages
- Simple CLI-driven analysis and launch
- Seamless navigation from graph to source code
- Architecture designed for future IDE integration

## 3. Non-Goals (MVP)

- Guaranteed accuracy of a full call graph
- Runtime data-flow analysis
- Tree-sitter-based fast analysis
- Real-time incremental analysis
- Optimization for very large repositories

## 4. Target Users

- Software engineers
- Developers who need to understand large codebases
- Teams looking to visualize architecture

## 5. User Flow

### 5.1 CLI Mode

```bash
depgraph ./my-project
```

1. Scan the target directory
2. Start LSP and analyze
3. Generate graph data
4. Start local server
5. Open browser

### 5.2 Web UI Mode

1. Display graph (React Flow)
2. Click a node
3. Show side panel or modal
4. Display code editor with range highlight

## 6. Functional Requirements

### 6.1 Analysis (MVP)

- File enumeration
- Symbol listing (`textDocument/documentSymbol`)
- Definition lookup (`textDocument/definition`)
- Reference search (`textDocument/references`)

### 6.2 Graph Generation

```ts
type Graph = {
  nodes: Node[];
  edges: Edge[];
};
```

- Node types: `file` / `symbol`
- Edge types:
  - `defines`
  - `references`

### 6.3 UI (React Flow)

- Node rendering
- Edge rendering
- Zoom / pan
- Node click events

### 6.4 Code Viewer

- Web-based editor (Monaco)
- File content display
- Range highlighting
- Read-only

### 6.5 CLI

```bash
depgraph <target-dir>
depgraph serve <target-dir>
```

## 7. Non-Functional Requirements

- Cross-platform (macOS / Linux)
- Single binary distribution
- Response < 2s for medium-sized projects
- UI maintains 60 fps

## 8. Architecture

### 8.1 High-Level

```
[ CLI ]
   ↓
[ Application Core ]
   ↓
[ Ports ]
   ↓
[ Adapters ]
   ├─ LSP Adapter
   ├─ FileSystem Adapter
   ├─ HTTP Server Adapter
   └─ Future: IDE Adapter
```

### 8.2 Port Definitions

```go
type AnalyzerPort interface {
    Analyze(ctx context.Context, root string) (Graph, error)
}

type EditorPort interface {
    GetFileContent(path string) (string, error)
}

type ServerPort interface {
    Serve(graph Graph) error
}
```

### 8.3 Adapter Examples

- LSP Adapter
- CLI Adapter
- HTTP Adapter
- IDE Adapter (future)

## 9. Data Model

```ts
type Node = {
  id: string;
  kind: "file" | "symbol";
  label: string;
  path?: string;
  range?: Range;
};

type Edge = {
  id: string;
  from: string;
  to: string;
  kind: "defines" | "references";
  confidence: "exact" | "probable";
};
```

## 10. API Design

### 10.1 Get graph

```http
GET /graph
```

### 10.2 Get file

```http
GET /file?path=...
```

## 11. Tech Stack

### Backend

- Go
- JSON-RPC client (LSP)
- net/http

### Frontend

- React
- React Flow
- Monaco Editor

## 12. Future Extensions

- Tree-sitter-based fast analysis
- Call graph (LSP `callHierarchy`)
- Runtime trace (DAP)
- IDE plugins (VSCode / Zed / Neovim)
- Incremental analysis
- Graph diff

## 13. Risks

- Accuracy issues due to LSP implementation differences
- Performance on large projects
- Behavioral differences across languages

## 14. Success Metrics

- First-run analysis success rate > 90%
- UI interaction response < 100ms
- Support for major languages (TypeScript / Go / Rust)

## 15. Milestones

### Phase 1 (MVP)

- CLI implementation
- LSP connection
- Graph JSON output
- Web UI display

### Phase 2

- Code viewer integration
- Node click ↔ editor range linkage

### Phase 3

- IDE Adapter
- Tree-sitter integration
