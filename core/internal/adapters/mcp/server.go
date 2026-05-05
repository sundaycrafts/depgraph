package mcp

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
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

//go:embed tools.json
var toolsJSON json.RawMessage

type warmupState int

const (
	stateIdle    warmupState = iota // warmup not yet called
	stateRunning                    // analysis in progress
	stateReady                      // graph loaded and ready
	stateFailed                     // analysis ended with error
)

// analysisEntry holds all per-root analysis state.
type analysisEntry struct {
	mu           sync.RWMutex
	state        warmupState
	warmupErr    error
	warmupCancel context.CancelFunc
	graph        domain.Graph
	editor       ports.EditorPort
	nodeByID     map[string]domain.Node
	refsByTo     map[string][]string // edge.To → []edge.From for "references" edges
}

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
	Result  mcpResult       `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

// mcpResult is the success-result of an MCP method dispatch.
// Sealed within the package via the unexported marker method.
type mcpResult interface {
	isMcpResult()
}

func (initializeResult) isMcpResult() {}
func (toolsListResult) isMcpResult()  {}
func (toolCallResult) isMcpResult()   {}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP method-result types.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
}

type serverCapabilities struct {
	Tools struct{} `json:"tools"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools json.RawMessage `json:"tools"`
}

type toolCallResult struct {
	Content []toolCallContent `json:"content"`
}

type toolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Adapter implements ports.ServerPort and serves MCP over stdio.
type Adapter struct {
	analyzerFactory func(excludes []string) ports.AnalyzerPort
	editorFactory   func(string) ports.EditorPort

	analysesMu sync.RWMutex
	analyses   map[string]*analysisEntry // keyed by root path

	in     io.Reader
	out    io.Writer
	sendMu sync.Mutex // serialises writes to out
}

// New creates an Adapter that accepts a target directory and optional exclude globs at runtime via the warmup tool.
func New(analyzerFactory func(excludes []string) ports.AnalyzerPort, editorFactory func(string) ports.EditorPort) *Adapter {
	return &Adapter{
		analyzerFactory: analyzerFactory,
		editorFactory:   editorFactory,
		analyses:        make(map[string]*analysisEntry),
		in:              os.Stdin,
		out:             os.Stdout,
	}
}

// newWithIO is used in tests with a pre-built graph.
func newWithIO(root string, graph domain.Graph, editor ports.EditorPort, in io.Reader, out io.Writer) *Adapter {
	entry := &analysisEntry{editor: editor}
	loadGraphIntoEntry(entry, graph)
	entry.state = stateReady
	return &Adapter{
		analyses: map[string]*analysisEntry{root: entry},
		in:       in,
		out:      out,
	}
}

func loadGraphIntoEntry(entry *analysisEntry, graph domain.Graph) {
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
	entry.graph = graph
	entry.nodeByID = nodeByID
	entry.refsByTo = refsByTo
}

// getReadyEntry looks up the analysis for root and returns it if ready, or an error otherwise.
func (a *Adapter) getReadyEntry(root string) (*analysisEntry, *rpcErr) {
	a.analysesMu.RLock()
	entry := a.analyses[root]
	a.analysesMu.RUnlock()

	if entry == nil {
		return nil, &rpcErr{Code: -32603, Message: "call warmup first for root: " + root}
	}

	entry.mu.RLock()
	st := entry.state
	warmupErr := entry.warmupErr
	entry.mu.RUnlock()

	switch st {
	case stateReady:
		return entry, nil
	case stateRunning, stateIdle:
		return nil, &rpcErr{Code: -32603, Message: "warmup is still running, retry shortly"}
	default: // stateFailed
		return nil, &rpcErr{Code: -32603, Message: "warmup failed: " + warmupErr.Error()}
	}
}

func (a *Adapter) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(a.in)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	scanner.Split(splitJSONLines)

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

func (a *Adapter) dispatch(ctx context.Context, msg rpcMsg) (mcpResult, *rpcErr) {
	switch msg.Method {
	case "initialize":
		return initializeResult{
			ProtocolVersion: mcpProtocolVersion,
			ServerInfo:      serverInfo{Name: "depgraph", Version: version.Version},
		}, nil

	case "tools/list":
		return toolsListResult{Tools: toolsJSON}, nil

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

func (a *Adapter) handleToolCall(ctx context.Context, raw json.RawMessage) (mcpResult, *rpcErr) {
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

		a.analysesMu.Lock()
		entry, exists := a.analyses[args.Root]
		if !exists {
			entry = &analysisEntry{}
			a.analyses[args.Root] = entry
		}
		a.analysesMu.Unlock()

		entry.mu.Lock()
		if entry.warmupCancel != nil {
			entry.warmupCancel()
		}
		wCtx, cancel := context.WithCancel(ctx)
		entry.warmupCancel = cancel
		entry.state = stateRunning
		entry.warmupErr = nil
		entry.mu.Unlock()

		go func() {
			defer cancel()
			graph, err := a.analyzerFactory(args.Excludes).Analyze(wCtx, args.Root)
			entry.mu.Lock()
			defer entry.mu.Unlock()
			if wCtx.Err() != nil {
				return // cancelled by a subsequent warmup call for the same root
			}
			if err != nil {
				entry.state = stateFailed
				entry.warmupErr = err
				return
			}
			entry.editor = a.editorFactory(args.Root)
			loadGraphIntoEntry(entry, graph)
			entry.state = stateReady
		}()

		text = `{"status":"warming_up"}`

	case "find_references":
		var args struct {
			Root     string `json:"root"`
			SymbolID string `json:"symbol_id"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || args.Root == "" {
			return nil, &rpcErr{Code: -32602, Message: "root required"}
		}
		if args.SymbolID == "" {
			return nil, &rpcErr{Code: -32602, Message: "symbol_id required"}
		}
		entry, rpcError := a.getReadyEntry(args.Root)
		if rpcError != nil {
			return nil, rpcError
		}
		nodes := a.findReferences(entry, args.SymbolID)
		b, _ := json.Marshal(nodes)
		text = string(b)

	case "find_symbols":
		var args struct {
			Root  string `json:"root"`
			Query string `json:"query"`
		}
		if err := json.Unmarshal(p.Arguments, &args); err != nil || args.Root == "" {
			return nil, &rpcErr{Code: -32602, Message: "root required"}
		}
		entry, rpcError := a.getReadyEntry(args.Root)
		if rpcError != nil {
			return nil, rpcError
		}
		nodes := a.findSymbols(entry, args.Query)
		b, _ := json.Marshal(nodes)
		text = string(b)

	default:
		return nil, &rpcErr{Code: -32602, Message: "unknown tool: " + p.Name}
	}

	return toolCallResult{
		Content: []toolCallContent{{Type: "text", Text: text}},
	}, nil
}

// findSymbols returns all symbol nodes in entry whose Label fuzzy-matches query.
// An empty query returns all symbols.
func (a *Adapter) findSymbols(entry *analysisEntry, query string) []domain.Node {
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	var result []domain.Node
	for _, n := range entry.graph.Nodes {
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

// findReferences performs BFS upstream within entry: given a symbol ID, returns
// all nodes that transitively reference it (i.e. the caller chain leading to symbolID).
func (a *Adapter) findReferences(entry *analysisEntry, symbolID string) []domain.Node {
	entry.mu.RLock()
	if _, ok := entry.nodeByID[symbolID]; !ok {
		entry.mu.RUnlock()
		return nil
	}
	// Maps are replaced wholesale on each warmup (never mutated in-place),
	// so capturing references and releasing the lock before BFS is safe.
	refsByTo := entry.refsByTo
	nodeByID := entry.nodeByID
	entry.mu.RUnlock()

	seen := map[string]bool{symbolID: true}
	queue := []string{symbolID}
	var result []domain.Node
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, fromID := range refsByTo[cur] {
			if seen[fromID] {
				continue
			}
			seen[fromID] = true
			queue = append(queue, fromID)
			if n, ok := nodeByID[fromID]; ok {
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

// splitJSONLines is a bufio.SplitFunc that yields one JSON value per newline (NDJSON / JSON Lines).
// Used by the MCP 2025-11-25 stdio transport.
func splitJSONLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, bytes.TrimSpace(data[:i]), nil
	}
	if atEOF && len(data) > 0 {
		return len(data), bytes.TrimSpace(data), nil
	}
	return 0, nil, nil
}
