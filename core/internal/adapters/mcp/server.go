package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/ports"
	"github.com/sundaycrafts/depgraph/internal/version"
)

const mcpProtocolVersion = "2025-11-25"

type warmupState int

const (
	stateIdle    warmupState = iota // warmup not yet called
	stateRunning                    // analysis in progress
	stateReady                      // graph loaded and ready
	stateFailed                     // analysis ended with error
)

// rpcMsg is an incoming JSON-RPC 2.0 message (request or notification).
type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Adapter implements ports.ServerPort and serves MCP over stdio.
type Adapter struct {
	analyzerFactory func(excludes []string) ports.AnalyzerPort
	editorFactory   func(string) ports.EditorPort

	graphMu      sync.RWMutex       // protects all graph state fields below
	state        warmupState
	warmupErr    error
	warmupCancel context.CancelFunc // cancels the in-flight warmup goroutine
	graph        domain.Graph
	editor       ports.EditorPort
	nodeByID     map[string]domain.Node
	refsByTo     map[string][]string // edge.To → []edge.From for "references" edges

	in     io.Reader
	out    io.Writer
	sendMu sync.Mutex // serialises writes to out
}

// New creates an Adapter that accepts a target directory and optional exclude globs at runtime via the warmup tool.
func New(analyzerFactory func(excludes []string) ports.AnalyzerPort, editorFactory func(string) ports.EditorPort) *Adapter {
	return &Adapter{
		analyzerFactory: analyzerFactory,
		editorFactory:   editorFactory,
		in:              os.Stdin,
		out:             os.Stdout,
	}
}

// newWithIO is used in tests with a pre-built graph.
func newWithIO(graph domain.Graph, editor ports.EditorPort, in io.Reader, out io.Writer) *Adapter {
	a := &Adapter{in: in, out: out, editor: editor}
	a.loadGraph(graph)
	a.state = stateReady
	return a
}

func (a *Adapter) loadGraph(graph domain.Graph) {
	nodeByID := make(map[string]domain.Node, len(graph.Nodes))
	for _, n := range graph.Nodes {
		nodeByID[n.ID] = n
	}
	refsByTo := make(map[string][]string)
	for _, e := range graph.Edges {
		if e.Kind == domain.EdgeKindReferences {
			refsByTo[e.To] = append(refsByTo[e.To], e.From)
		}
	}
	a.graph = graph
	a.nodeByID = nodeByID
	a.refsByTo = refsByTo
}

// requireReady is the gateway for tools that need a loaded graph.
func (a *Adapter) requireReady() *rpcErr {
	a.graphMu.RLock()
	defer a.graphMu.RUnlock()
	switch a.state {
	case stateIdle:
		return &rpcErr{Code: -32603, Message: "call warmup first"}
	case stateRunning:
		return &rpcErr{Code: -32603, Message: "warmup is still running, retry shortly"}
	case stateFailed:
		return &rpcErr{Code: -32603, Message: "warmup failed: " + a.warmupErr.Error()}
	}
	return nil
}

func (a *Adapter) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(a.in)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	scanner.Split(splitMCP)

	done := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			var msg rpcMsg
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			// Notifications have no id field.
			if msg.ID == nil {
				continue
			}
			result, rpcError := a.dispatch(ctx, msg)
			resp := rpcResp{JSONRPC: "2.0", ID: msg.ID}
			if rpcError != nil {
				resp.Error = rpcError
			} else {
				resp.Result = result
			}
			a.send(resp)
		}
		done <- scanner.Err()
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		return err
	}
}

func (a *Adapter) dispatch(ctx context.Context, msg rpcMsg) (any, *rpcErr) {
	switch msg.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "depgraph", "version": version.Version},
		}, nil

	case "tools/list":
		return map[string]any{"tools": toolDefinitions()}, nil

	case "tools/call":
		return a.handleToolCall(ctx, msg.Params)

	default:
		return nil, &rpcErr{Code: -32601, Message: "method not found: " + msg.Method}
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (a *Adapter) handleToolCall(ctx context.Context, raw json.RawMessage) (any, *rpcErr) {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid params"}
	}

	var text string
	switch p.Name {
	case "warmup":
		var args struct {
			Root     string   `json:"root"`
			Excludes []string `json:"excludes"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || args.Root == "" {
			return nil, &rpcErr{Code: -32602, Message: "root required"}
		}
		if a.analyzerFactory == nil {
			return nil, &rpcErr{Code: -32603, Message: "analyzer not available"}
		}

		a.graphMu.Lock()
		if a.warmupCancel != nil {
			a.warmupCancel()
		}
		wCtx, cancel := context.WithCancel(ctx)
		a.warmupCancel = cancel
		a.state = stateRunning
		a.warmupErr = nil
		a.graphMu.Unlock()

		go func() {
			defer cancel()
			graph, err := a.analyzerFactory(args.Excludes).Analyze(wCtx, args.Root)
			a.graphMu.Lock()
			defer a.graphMu.Unlock()
			if wCtx.Err() != nil {
				return // cancelled by a subsequent warmup call
			}
			if err != nil {
				a.state = stateFailed
				a.warmupErr = err
				return
			}
			a.editor = a.editorFactory(args.Root)
			a.loadGraph(graph)
			a.state = stateReady
		}()

		text = `{"status":"warming_up"}`

	case "find_references":
		if err := a.requireReady(); err != nil {
			return nil, err
		}
		var args struct {
			SymbolID string `json:"symbol_id"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || args.SymbolID == "" {
			return nil, &rpcErr{Code: -32602, Message: "symbol_id required"}
		}
		nodes := a.findReferences(args.SymbolID)
		b, _ := json.Marshal(nodes)
		text = string(b)

	case "find_symbols":
		if err := a.requireReady(); err != nil {
			return nil, err
		}
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return nil, &rpcErr{Code: -32602, Message: "invalid params"}
		}
		nodes := a.findSymbols(args.Query)
		b, _ := json.Marshal(nodes)
		text = string(b)

	case "read_file":
		if err := a.requireReady(); err != nil {
			return nil, err
		}
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || args.Path == "" {
			return nil, &rpcErr{Code: -32602, Message: "path required"}
		}
		content, err := a.editor.GetFileContent(args.Path)
		if err != nil {
			return nil, &rpcErr{Code: -32603, Message: err.Error()}
		}
		text = content

	default:
		return nil, &rpcErr{Code: -32602, Message: "unknown tool: " + p.Name}
	}

	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}, nil
}

// findSymbols returns all symbol nodes whose Label fuzzy-matches query.
// An empty query returns all symbols.
func (a *Adapter) findSymbols(query string) []domain.Node {
	var result []domain.Node
	for _, n := range a.graph.Nodes {
		if n.Kind == domain.NodeKindSymbol && fuzzyMatch(query, n.Label) {
			result = append(result, n)
		}
	}
	return result
}

// fuzzyMatch reports whether all runes of query appear in target in order
// (case-insensitive). An empty query matches everything.
func fuzzyMatch(query, target string) bool {
	query = strings.ToLower(query)
	target = strings.ToLower(target)
	qi := 0
	for _, c := range target {
		if qi < len(query) && rune(query[qi]) == c {
			qi++
		}
	}
	return qi == len(query)
}

// findReferences performs BFS upstream: given a symbol ID, returns all nodes
// that transitively reference it (i.e. the caller chain leading to symbolID).
func (a *Adapter) findReferences(symbolID string) []domain.Node {
	seen := map[string]bool{symbolID: true}
	queue := []string{symbolID}
	var result []domain.Node
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, fromID := range a.refsByTo[cur] {
			if seen[fromID] {
				continue
			}
			seen[fromID] = true
			queue = append(queue, fromID)
			if n, ok := a.nodeByID[fromID]; ok {
				result = append(result, n)
			}
		}
	}
	return result
}

func (a *Adapter) send(resp rpcResp) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.out.Write(append(data, '\n')) //nolint:errcheck
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "warmup",
			"description": "Analyze the project at the given root and load the dependency graph. Runs asynchronously — returns immediately with {\"status\":\"warming_up\"} and loads the graph in the background. Call find_symbols, find_references, or read_file after warmup; they return a \"retry shortly\" error while analysis is in progress. Re-calling warmup with a different root or excludes cancels any in-flight analysis and restarts. Exclude test files and generated code to keep the graph focused (e.g. excludes: [\"**/*_test.go\", \"**/*.gen.go\", \"**/*.test.ts\", \"**/*.spec.ts\"]).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"root": map[string]any{
						"type":        "string",
						"description": "Absolute path to the project root directory to analyze",
					},
					"excludes": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Doublestar glob patterns relative to root to exclude from analysis (e.g. [\"**/*_test.go\", \"node_modules/**\"])",
					},
				},
				"required": []string{"root"},
			},
		},
		{
			"name":        "find_references",
			"description": "Recursively find all symbols that (transitively) reference the given symbol. Returns the upstream caller chain.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol_id": map[string]any{
						"type":        "string",
						"description": "Node ID of the target symbol (obtain IDs from find_symbols)",
					},
				},
				"required": []string{"symbol_id"},
			},
		},
		{
			"name":        "find_symbols",
			"description": "Search for symbols in the dependency graph by name using fuzzy matching. Returns matching symbols with their IDs, labels, kinds, and file paths. Use the returned ID with find_references.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Fuzzy search query matched against symbol names (case-insensitive subsequence match). Empty string returns all symbols.",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "read_file",
			"description": "Read the contents of a source file.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the file",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

// splitMCP is a bufio.SplitFunc for newline-delimited JSON (MCP 2025-11-25 stdio transport).
func splitMCP(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, bytes.TrimSpace(data[:i]), nil
	}
	if atEOF && len(data) > 0 {
		return len(data), bytes.TrimSpace(data), nil
	}
	return 0, nil, nil
}
