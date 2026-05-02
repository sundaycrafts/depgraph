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
	graph    domain.Graph
	editor   ports.EditorPort
	nodeByID map[string]domain.Node
	refsByTo map[string][]string // edge.To → []edge.From for "references" edges
	in       io.Reader
	out      io.Writer
	mu       sync.Mutex // serialises writes to out
}

func New(graph domain.Graph, editor ports.EditorPort) *Adapter {
	return newWithIO(graph, editor, os.Stdin, os.Stdout)
}

func newWithIO(graph domain.Graph, editor ports.EditorPort, in io.Reader, out io.Writer) *Adapter {
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
	return &Adapter{
		graph:    graph,
		editor:   editor,
		nodeByID: nodeByID,
		refsByTo: refsByTo,
		in:       in,
		out:      out,
	}
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
			result, rpcError := a.dispatch(msg)
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

func (a *Adapter) dispatch(msg rpcMsg) (any, *rpcErr) {
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
		return a.handleToolCall(msg.Params)

	default:
		return nil, &rpcErr{Code: -32601, Message: "method not found: " + msg.Method}
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (a *Adapter) handleToolCall(raw json.RawMessage) (any, *rpcErr) {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid params"}
	}

	var text string
	switch p.Name {
	case "find_references":
		var args struct {
			SymbolID string `json:"symbol_id"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || args.SymbolID == "" {
			return nil, &rpcErr{Code: -32602, Message: "symbol_id required"}
		}
		nodes := a.findReferences(args.SymbolID)
		b, _ := json.Marshal(nodes)
		text = string(b)

	case "list_symbols":
		var symbols []domain.Node
		for _, n := range a.graph.Nodes {
			if n.Kind == domain.NodeKindSymbol {
				symbols = append(symbols, n)
			}
		}
		b, _ := json.Marshal(symbols)
		text = string(b)

	case "read_file":
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
			"name":        "find_references",
			"description": "Recursively find all symbols that (transitively) reference the given symbol. Returns the upstream caller chain.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol_id": map[string]any{
						"type":        "string",
						"description": "Node ID of the target symbol (obtain IDs from list_symbols)",
					},
				},
				"required": []string{"symbol_id"},
			},
		},
		{
			"name":        "list_symbols",
			"description": "List all symbols in the dependency graph with their IDs, labels, kinds, and file paths.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
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
