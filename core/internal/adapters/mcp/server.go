package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/ports"
)

const mcpProtocolVersion = "2024-11-05"

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
	analyzer      ports.AnalyzerPort
	editorFactory func(string) ports.EditorPort

	graph    domain.Graph
	editor   ports.EditorPort
	nodeByID map[string]domain.Node
	refsByTo map[string][]string // edge.To → []edge.From for "references" edges

	in  io.Reader
	out io.Writer
	mu  sync.Mutex // serialises writes to out
}

// New creates an Adapter that accepts a target directory at runtime via the set_root tool.
func New(analyzer ports.AnalyzerPort, editorFactory func(string) ports.EditorPort) *Adapter {
	return &Adapter{
		analyzer:      analyzer,
		editorFactory: editorFactory,
		in:            os.Stdin,
		out:           os.Stdout,
	}
}

// newWithIO is used in tests with a pre-built graph.
func newWithIO(graph domain.Graph, editor ports.EditorPort, in io.Reader, out io.Writer) *Adapter {
	a := &Adapter{in: in, out: out, editor: editor}
	a.loadGraph(graph)
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
			"serverInfo":      map[string]any{"name": "depgraph", "version": "0.1.0"},
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
	case "root":
		var args struct {
			Root string `json:"root"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || args.Root == "" {
			return nil, &rpcErr{Code: -32602, Message: "root required"}
		}
		if a.analyzer == nil {
			return nil, &rpcErr{Code: -32603, Message: "analyzer not available"}
		}
		graph, err := a.analyzer.Analyze(ctx, args.Root)
		if err != nil {
			return nil, &rpcErr{Code: -32603, Message: err.Error()}
		}
		a.editor = a.editorFactory(args.Root)
		a.loadGraph(graph)
		text = fmt.Sprintf(`{"nodes":%d,"edges":%d}`, len(graph.Nodes), len(graph.Edges))

	case "find_references":
		if a.nodeByID == nil {
			return nil, &rpcErr{Code: -32603, Message: "no project loaded; call the root tool first"}
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
		if a.nodeByID == nil {
			return nil, &rpcErr{Code: -32603, Message: "no project loaded; call the root tool first"}
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
		if a.editor == nil {
			return nil, &rpcErr{Code: -32603, Message: "no project loaded; call the root tool first"}
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
	a.mu.Lock()
	defer a.mu.Unlock()
	fmt.Fprintf(a.out, "Content-Length: %d\r\n\r\n%s", len(data), data)
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "root",
			"description": "Set the project root directory to analyze. Must be called before using find_symbols, find_references, or read_file. Re-calling with a different root switches to that project.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"root": map[string]any{
						"type":        "string",
						"description": "Absolute path to the project root directory to analyze",
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

// splitMCP is a bufio.SplitFunc for Content-Length framed JSON messages
// (same framing as LSP, used by MCP stdio transport).
func splitMCP(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	headerEnd := strings.Index(string(data), "\r\n\r\n")
	if headerEnd < 0 {
		if atEOF {
			return 0, nil, fmt.Errorf("MCP: unexpected EOF in header")
		}
		return 0, nil, nil
	}
	header := string(data[:headerEnd])
	contentLen := -1
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLen, err = strconv.Atoi(val)
			if err != nil {
				return 0, nil, fmt.Errorf("MCP: invalid Content-Length: %w", err)
			}
		}
	}
	if contentLen < 0 {
		return 0, nil, fmt.Errorf("MCP: missing Content-Length header")
	}
	start := headerEnd + 4
	end := start + contentLen
	if end > len(data) {
		if atEOF {
			return 0, nil, fmt.Errorf("MCP: unexpected EOF in body")
		}
		return 0, nil, nil
	}
	return end, data[start:end], nil
}
