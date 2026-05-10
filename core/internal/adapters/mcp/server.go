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

	"github.com/sundaycrafts/depgraph/internal/config"
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
	Warnings        []string           `json:"warnings,omitempty"`
	// Projects is the canonical list of roots the agent can pass to
	// find_symbols / find_references. Populated post-registration so
	// config-loaded, legacy-auto-added, and any pre-existing roots all
	// appear. Without this the agent has no discovery path: error
	// messages and tool schemas alone are circular ("the root passed
	// to add_project").
	Projects []projectInfo `json:"projects,omitempty"`
}

// projectInfo is the agent-visible projection of a registered Project.
// Languages helps the agent reason about which symbol kinds to expect
// without an extra round-trip.
type projectInfo struct {
	Root      string   `json:"root"`
	Languages []string `json:"languages"`
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

	// configCWD is the directory depgraph.yaml is loaded from. Captured
	// at startup so reload_config reads the same file even if some tool
	// later changes the process working directory.
	configCWD string
	// cfgMu guards startupCfg / startupWarn — both are replaced wholesale
	// by reload_config from a separate goroutine relative to initialize.
	cfgMu       sync.Mutex
	startupCfg  *config.Config
	startupWarn []string

	in     io.Reader
	out    io.Writer
	sendMu sync.Mutex // serialises writes to out
}

// New builds an Adapter wired to workspace and initializer. cfg and
// warnings come from config.Load on the launch directory; the Adapter
// uses them during the initialize handshake and re-uses configCWD for
// reload_config. cfg may be nil (no depgraph.yaml found) in which case
// initialize falls back to the legacy CWD marker-file auto-add.
//
// The Adapter takes ownership of workspace: Workspace.Shutdown is
// invoked from Serve before returning.
func New(
	workspace *domain.Workspace,
	initializer ProjectInitializer,
	cfg *config.Config,
	warnings []string,
	configCWD string,
	logger *slog.Logger,
) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		workspace:   workspace,
		initializer: initializer,
		logger:      logger,
		startupCfg:  cfg,
		startupWarn: append([]string(nil), warnings...),
		configCWD:   configCWD,
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
		warnings := a.handleInitializeProjects(ctx)
		return initializeResult{
			ProtocolVersion: mcpProtocolVersion,
			ServerInfo:      serverInfo{Name: "depgraph", Version: version.Version},
			Instructions: "Each project root registered with depgraph is a 'project'. The set of " +
				"currently registered roots is returned in this response's `projects` field — pass " +
				"one of those exact paths to find_symbols / find_references. depgraph reads " +
				"depgraph.yaml from the launch directory at startup; every entry there is registered " +
				"automatically. Without a config file, a project is auto-registered if the launch " +
				"directory contains a supported marker file (go.mod, Cargo.toml, tsconfig.json). " +
				"Call add_project for any extra root not in the file, and reload_config after editing " +
				"depgraph.yaml to pick up newly added entries (its response also returns the updated " +
				"projects list). Projects index asynchronously: find_symbols and find_references " +
				"return a 'retry shortly' error until indexing finishes.",
			Warnings: warnings,
			Projects: a.registeredProjects(),
		}, nil

	case "tools/list":
		return toolsListResult{Tools: toolsJSON}, nil

	case "tools/call":
		return a.handleToolCall(ctx, msg.Params)

	default:
		return nil, &rpcErr{Code: -32601, Message: "method not found: " + msg.Method}
	}
}

// handleInitializeProjects registers projects at startup. When a parsed
// depgraph.yaml is available, every entry in it is registered. Otherwise
// the legacy CWD marker-file fallback runs so users without a config
// file still get auto-registration. Returns warnings to attach to the
// initialize response — config-load issues plus per-project AddProject
// errors.
func (a *Adapter) handleInitializeProjects(ctx context.Context) []string {
	a.cfgMu.Lock()
	cfg := a.startupCfg
	warn := append([]string(nil), a.startupWarn...)
	a.cfgMu.Unlock()

	if cfg != nil {
		warn = append(warn, a.applyConfig(cfg)...)
		return warn
	}
	warn = append(warn, a.legacyCWDAutoAdd(ctx)...)
	return warn
}

// applyConfig registers each project listed in cfg. Returns one warning
// string per AddProject failure; success is silent. Used by initialize
// and reload_config.
func (a *Adapter) applyConfig(cfg *config.Config) []string {
	var warnings []string
	for _, p := range cfg.Projects {
		if _, err := a.workspace.AddProject(p.Root, p.Excludes, p.ExcludeSymbols); err != nil {
			warnings = append(warnings, fmt.Sprintf("add_project %q: %v", p.Root, err))
			a.logger.Warn("config add_project failed", "root", p.Root, "err", err)
			continue
		}
		a.logger.Info("registered project from config",
			"root", p.Root,
			"excludes", len(p.Excludes),
			"exclude_symbols", len(p.ExcludeSymbols))
	}
	return warnings
}

// legacyCWDAutoAdd is the pre-config-file behaviour: register the launch
// directory if it carries a recognised marker file. Kept so users
// without a depgraph.yaml are not forced to write one. Errors are
// surfaced as warnings but never abort the handshake.
func (a *Adapter) legacyCWDAutoAdd(ctx context.Context) []string {
	cwd, err := os.Getwd()
	if err != nil {
		a.logger.Warn("auto-add: getwd failed; agent must call add_project manually", "err", err)
		return []string{fmt.Sprintf("auto-add: getwd failed: %v", err)}
	}
	langs, err := lsploader.Detect(cwd)
	if err != nil {
		a.logger.Warn("auto-add: language detection failed", "cwd", cwd, "err", err)
		return []string{fmt.Sprintf("auto-add: detect %q: %v", cwd, err)}
	}
	if len(langs) == 0 {
		a.logger.Info("auto-add: no marker files in cwd; agent must call add_project manually", "cwd", cwd)
		return nil
	}
	if _, err := a.workspace.AddProject(cwd, nil, nil); err != nil {
		a.logger.Warn("auto-add project failed", "cwd", cwd, "err", err)
		return []string{fmt.Sprintf("auto-add %q: %v", cwd, err)}
	}
	a.logger.Info("auto-registered project from cwd", "cwd", cwd, "langs", langs)

	if a.initializer != nil {
		if err := a.initializer.Initialize(ctx, cwd, langs, a.workspace); err != nil {
			a.logger.Warn("project initializer failed", "err", err)
			return []string{fmt.Sprintf("initializer: %v", err)}
		}
	}
	return nil
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
	case "reload_config":
		return a.handleReloadConfig()
	default:
		return nil, &rpcErr{Code: -32602, Message: "unknown tool: " + p.Name}
	}
}

// handleReloadConfig re-reads depgraph.yaml from the launch directory
// and registers any newly listed projects via Workspace.AddProject.
// Existing projects are no-ops on duplicate root, so this call is
// idempotent — but their excludes / exclude_symbols are NOT updated;
// restart the server to pick up edits to an already-registered project.
func (a *Adapter) handleReloadConfig() (mcpResult, *rpcErr) {
	cfg, loadWarn := config.Load(a.configCWD)
	a.cfgMu.Lock()
	a.startupCfg = cfg
	a.startupWarn = append([]string(nil), loadWarn...)
	a.cfgMu.Unlock()

	warnings := append([]string(nil), loadWarn...)
	if cfg != nil {
		warnings = append(warnings, a.applyConfig(cfg)...)
	}

	payload := struct {
		Status   string        `json:"status"`
		Warnings []string      `json:"warnings,omitempty"`
		Projects []projectInfo `json:"projects,omitempty"`
	}{
		Status:   "reloaded",
		Warnings: warnings,
		Projects: a.registeredProjects(),
	}
	return marshalToolResult(payload)
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

// lookupProject resolves root and returns the matching Project. On miss
// the error message lists every currently-registered root so the agent
// can self-correct without a separate discovery round-trip.
func (a *Adapter) lookupProject(root string) (*domain.Project, *rpcErr) {
	project := a.workspace.Get(root)
	if project != nil {
		return project, nil
	}
	msg := "call add_project first for root: " + root
	if list := a.workspace.List(); len(list) > 0 {
		roots := make([]string, len(list))
		for i, p := range list {
			roots[i] = p.Root
		}
		msg += fmt.Sprintf(" (registered: %v)", roots)
	}
	return nil, &rpcErr{Code: -32603, Message: msg}
}

// registeredProjects projects every entry in workspace.List() into the
// agent-visible projectInfo shape. Empty list when nothing is
// registered (returned as `projects: []` is suppressed by omitempty).
func (a *Adapter) registeredProjects() []projectInfo {
	list := a.workspace.List()
	out := make([]projectInfo, 0, len(list))
	for _, p := range list {
		langs := make([]string, len(p.Languages))
		for i, l := range p.Languages {
			langs[i] = string(l)
		}
		out = append(out, projectInfo{Root: p.Root, Languages: langs})
	}
	return out
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
