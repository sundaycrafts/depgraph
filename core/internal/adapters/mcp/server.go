package mcp

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
	"github.com/sundaycrafts/depgraph/internal/version"
)

const mcpProtocolVersion = "2025-11-25"

//go:embed tools.json
var toolsJSON json.RawMessage

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

// mcpResult is the success-result of an MCP method dispatch. Sealed within
// the package via the unexported marker method.
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

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
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

// Adapter implements domain.PortServer and serves MCP over stdio. All the
// real work (per-language LSP sessions, file watching, BFS over caller
// chains) lives in domain.Workspace and the adapters that satisfy its
// ports — Adapter is just JSON-RPC dispatch.
type Adapter struct {
	workspace   *domain.Workspace
	initializer ProjectInitializer
	logger      *slog.Logger

	in     io.Reader
	out    io.Writer
	sendMu sync.Mutex // serialises writes to out
}

// New builds an Adapter wired to workspace and initializer. The Adapter
// takes ownership of workspace: Workspace.Shutdown is invoked from Serve
// before returning.
func New(workspace *domain.Workspace, initializer ProjectInitializer, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		workspace:   workspace,
		initializer: initializer,
		logger:      logger,
		in:          os.Stdin,
		out:         os.Stdout,
	}
}

// Serve runs the JSON-RPC dispatch loop until stdin closes or ctx is
// cancelled. Workspace.Shutdown is always called before return so every
// spawned LSP process and file watcher is reaped cleanly.
func (a *Adapter) Serve(ctx context.Context) error {
	defer a.workspace.Shutdown()
	if a.initializer != nil {
		defer func() {
			if err := a.initializer.Shutdown(context.Background()); err != nil {
				a.logger.Warn("initializer shutdown failed", "err", err)
			}
		}()
	}

	scanner := bufio.NewScanner(a.in)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	scanner.Split(splitJSONLines)

	done := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			var msg rpcMsg
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				// Malformed frame: cannot reply (no id available); log so the
				// stuck-state is debuggable from the operator's stderr.
				a.logger.Warn("dropping malformed JSON-RPC frame", "err", err, "frame", string(scanner.Bytes()))
				continue
			}
			if msg.ID == nil {
				continue // notification — nothing to reply to
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
		a.handleInitializeAutoProject(ctx)
		return initializeResult{
			ProtocolVersion: mcpProtocolVersion,
			ServerInfo:      serverInfo{Name: "depgraph", Version: version.Version},
			Instructions: "Each project root registered with depgraph is a 'project'. Call add_project for any " +
				"extra root you need; if depgraph was launched in a directory containing a supported marker file " +
				"(go.mod, Cargo.toml, tsconfig.json) that directory is registered automatically. Projects index " +
				"asynchronously: find_symbols and find_references return a 'retry shortly' error until indexing finishes.",
		}, nil

	case "tools/list":
		return toolsListResult{Tools: toolsJSON}, nil

	case "tools/call":
		return a.handleToolCall(ctx, msg.Params)

	default:
		return nil, &rpcErr{Code: -32601, Message: "method not found: " + msg.Method}
	}
}

// handleInitializeAutoProject registers the depgraph process's working
// directory as a project when it carries a recognised marker file.
// Errors are logged but never bubble up — failure to auto-register is a
// soft fallback (the agent can still call add_project manually).
func (a *Adapter) handleInitializeAutoProject(ctx context.Context) {
	cwd, err := os.Getwd()
	if err != nil {
		a.logger.Warn("auto-add: getwd failed; agent must call add_project manually", "err", err)
		return
	}
	langs, err := lsploader.Detect(cwd)
	if err != nil {
		a.logger.Warn("auto-add: language detection failed", "cwd", cwd, "err", err)
		return
	}
	if len(langs) == 0 {
		a.logger.Info("auto-add: no marker files in cwd; agent must call add_project manually", "cwd", cwd)
		return
	}
	if _, err := a.workspace.AddProject(cwd, nil, nil); err != nil {
		a.logger.Warn("auto-add project failed", "cwd", cwd, "err", err)
		return
	}
	a.logger.Info("auto-registered project from cwd", "cwd", cwd, "langs", langs)

	if a.initializer != nil {
		if err := a.initializer.Initialize(ctx, cwd, langs, a.workspace); err != nil {
			a.logger.Warn("project initializer failed", "err", err)
		}
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
	switch p.Name {
	case "add_project":
		return a.handleAddProject(p.Arguments)
	case "find_symbols":
		return a.handleFindSymbols(ctx, p.Arguments)
	case "find_references":
		return a.handleFindReferences(ctx, p.Arguments)
	default:
		return nil, &rpcErr{Code: -32602, Message: "unknown tool: " + p.Name}
	}
}

func (a *Adapter) handleAddProject(raw json.RawMessage) (mcpResult, *rpcErr) {
	var args struct {
		Root           string   `json:"root"`
		Excludes       []string `json:"excludes"`
		ExcludeSymbols []string `json:"exclude_symbols"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid arguments: " + err.Error()}
	}
	if args.Root == "" {
		return nil, &rpcErr{Code: -32602, Message: "root required"}
	}
	if _, err := a.workspace.AddProject(args.Root, args.Excludes, args.ExcludeSymbols); err != nil {
		return nil, &rpcErr{Code: -32603, Message: err.Error()}
	}
	return toolCallResult{
		Content: []toolCallContent{{Type: "text", Text: `{"status":"indexing"}`}},
	}, nil
}

func (a *Adapter) handleFindSymbols(ctx context.Context, raw json.RawMessage) (mcpResult, *rpcErr) {
	var args struct {
		Root  string `json:"root"`
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid arguments: " + err.Error()}
	}
	if args.Root == "" {
		return nil, &rpcErr{Code: -32602, Message: "root required"}
	}
	project, rpcError := a.lookupProject(args.Root)
	if rpcError != nil {
		return nil, rpcError
	}
	payload, err := project.FindSymbols(ctx, args.Query)
	if err != nil {
		return nil, &rpcErr{Code: -32603, Message: err.Error()}
	}
	return marshalToolResult(payload)
}

func (a *Adapter) handleFindReferences(ctx context.Context, raw json.RawMessage) (mcpResult, *rpcErr) {
	var args struct {
		Root     string `json:"root"`
		SymbolID string `json:"symbol_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid arguments: " + err.Error()}
	}
	if args.Root == "" {
		return nil, &rpcErr{Code: -32602, Message: "root required"}
	}
	if args.SymbolID == "" {
		return nil, &rpcErr{Code: -32602, Message: "symbol_id required"}
	}
	target, err := domain.DecodeSymbolID(args.SymbolID)
	if err != nil {
		return nil, &rpcErr{Code: -32602, Message: err.Error()}
	}
	project, rpcError := a.lookupProject(args.Root)
	if rpcError != nil {
		return nil, rpcError
	}
	payload, err := project.FindReferences(ctx, target)
	if err != nil {
		return nil, &rpcErr{Code: -32603, Message: err.Error()}
	}
	return marshalToolResult(payload)
}

// lookupProject resolves root and returns the matching Project. Returns a
// JSON-RPC error if no project is registered for that root — the agent
// must call add_project first.
func (a *Adapter) lookupProject(root string) (*domain.Project, *rpcErr) {
	project := a.workspace.Get(root)
	if project == nil {
		return nil, &rpcErr{Code: -32603, Message: "call add_project first for root: " + root}
	}
	return project, nil
}

// marshalToolResult JSON-encodes payload and wraps it in the toolCallResult
// content shape.
//
// payload is always one of our internal types (PartialResult[Symbol],
// struct{...}) composed of string / int / []byte / json.RawMessage / slice
// / map fields, none of which can fail to marshal. A failure here therefore
// indicates a type-definition bug — crash loud rather than corrupt the
// wire protocol with empty content.
func marshalToolResult(payload any) (mcpResult, *rpcErr) {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("mcp: marshal tool result (programming bug): %v", err))
	}
	return toolCallResult{Content: []toolCallContent{{Type: "text", Text: string(b)}}}, nil
}

func (a *Adapter) send(resp rpcResp) {
	data, err := json.Marshal(resp)
	if err != nil {
		// rpcResp is composed of string / []byte / json.RawMessage / struct
		// fields with no exotic types — Marshal cannot fail for any value
		// we actually construct. A failure here means the type definition
		// has been changed in a backwards-incompatible way; panic so the
		// bug is caught in the first request that hits the new shape.
		panic(fmt.Sprintf("mcp: marshal JSON-RPC response (programming bug): %v", err))
	}
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if _, err := a.out.Write(append(data, '\n')); err != nil {
		// Stdout closed: the agent client most likely went away. Log once
		// per failure so a torn connection is visible.
		a.logger.Warn("write to MCP stdout failed", "err", err)
	}
}

// splitJSONLines is a bufio.SplitFunc that yields one JSON value per newline
// (NDJSON / JSON Lines). Used by the MCP 2025-11-25 stdio transport.
func splitJSONLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, bytes.TrimSpace(data[:i]), nil
	}
	if atEOF && len(data) > 0 {
		return len(data), bytes.TrimSpace(data), nil
	}
	return 0, nil, nil
}
