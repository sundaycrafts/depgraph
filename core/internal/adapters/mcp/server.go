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
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
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

// symbolPayload wraps the array of results returned by find_symbols and
// find_references with a list of human-readable warnings. Warnings are
// emitted whenever a downstream LSP call fails partway through aggregation
// or BFS — without them the agent has no signal that the result list is
// incomplete. The field is omitted when empty so the happy path stays
// concise: {"results":[...]}.
type symbolPayload struct {
	Results  []symbolResult `json:"results"`
	Warnings []string       `json:"warnings,omitempty"`
}

// symbolResult is the JSON shape of a single symbol returned by find_symbols
// and find_references.
type symbolResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Path      string `json:"path"` // path relative to component root
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

// Adapter implements ports.ServerPort and serves MCP over stdio.
type Adapter struct {
	manager     *ComponentManager
	initializer ProjectInitializer
	logger      *slog.Logger

	in     io.Reader
	out    io.Writer
	sendMu sync.Mutex // serialises writes to out
}

// New builds an Adapter wired to manager and initializer. The Adapter takes
// ownership of manager: ShutdownAll is invoked from Serve before returning.
func New(manager *ComponentManager, initializer ProjectInitializer, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		manager:     manager,
		initializer: initializer,
		logger:      logger,
		in:          os.Stdin,
		out:         os.Stdout,
	}
}

// Serve runs the JSON-RPC dispatch loop until stdin closes or ctx is
// cancelled. ComponentManager.ShutdownAll is always called before return so
// every spawned LSP process is reaped cleanly.
func (a *Adapter) Serve(ctx context.Context) error {
	defer a.manager.ShutdownAll()
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
		a.handleInitializeAutoComponent(ctx)
		return initializeResult{
			ProtocolVersion: mcpProtocolVersion,
			ServerInfo:      serverInfo{Name: "depgraph", Version: version.Version},
			Instructions: "Each project root is a 'component'. Call add_component for any extra root you need; " +
				"if depgraph was launched in a directory containing a supported marker file (go.mod, Cargo.toml, " +
				"tsconfig.json) that directory is registered automatically. Components index asynchronously: " +
				"find_symbols and find_references return a 'retry shortly' error until the component is ready.",
		}, nil

	case "tools/list":
		return toolsListResult{Tools: toolsJSON}, nil

	case "tools/call":
		return a.handleToolCall(ctx, msg.Params)

	default:
		return nil, &rpcErr{Code: -32601, Message: "method not found: " + msg.Method}
	}
}

// handleInitializeAutoComponent registers the depgraph process's working
// directory as a component when it carries a recognised marker file. Errors
// are logged but never bubble up — failure to auto-register is a soft
// fallback ("agent can still call add_component manually") rather than an
// MCP-level failure. Each branch logs at Warn so problems surface in the
// stderr stream that wraps the MCP session.
func (a *Adapter) handleInitializeAutoComponent(ctx context.Context) {
	cwd, err := os.Getwd()
	if err != nil {
		a.logger.Warn("auto-add: getwd failed; agent must call add_component manually", "err", err)
		return
	}
	langs, err := lsploader.Detect(cwd)
	if err != nil {
		a.logger.Warn("auto-add: language detection failed", "cwd", cwd, "err", err)
		return
	}
	if len(langs) == 0 {
		a.logger.Info("auto-add: no marker files in cwd; agent must call add_component manually", "cwd", cwd)
		return
	}
	if _, err := a.manager.AddComponent(cwd, nil); err != nil {
		a.logger.Warn("auto-add component failed", "cwd", cwd, "err", err)
		return
	}
	a.logger.Info("auto-registered component from cwd", "cwd", cwd, "langs", langs)

	if a.initializer != nil {
		if err := a.initializer.Initialize(ctx, cwd, langs, a.manager); err != nil {
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
	case "add_component":
		return a.handleAddComponent(p.Arguments)
	case "find_symbols":
		return a.handleFindSymbols(ctx, p.Arguments)
	case "find_references":
		return a.handleFindReferences(ctx, p.Arguments)
	default:
		return nil, &rpcErr{Code: -32602, Message: "unknown tool: " + p.Name}
	}
}

func (a *Adapter) handleAddComponent(raw json.RawMessage) (mcpResult, *rpcErr) {
	var args struct {
		Root     string   `json:"root"`
		Excludes []string `json:"excludes"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid arguments: " + err.Error()}
	}
	if args.Root == "" {
		return nil, &rpcErr{Code: -32602, Message: "root required"}
	}
	if _, err := a.manager.AddComponent(args.Root, args.Excludes); err != nil {
		return nil, &rpcErr{Code: -32603, Message: err.Error()}
	}
	return toolCallResult{
		Content: []toolCallContent{{Type: "text", Text: `{"status":"indexing"}`}},
	}, nil
}

func (a *Adapter) lookupReadyComponent(root string) (*Component, *rpcErr) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid root: " + err.Error()}
	}
	comp := a.manager.Get(abs)
	if comp == nil {
		return nil, &rpcErr{Code: -32603, Message: "call add_component first for root: " + abs}
	}
	state, stateErr := comp.State()
	switch state {
	case ComponentReady:
		return comp, nil
	case ComponentFailed:
		return nil, &rpcErr{Code: -32603, Message: "component failed: " + stateErr.Error()}
	default:
		return nil, &rpcErr{Code: -32603, Message: "component is indexing, retry shortly"}
	}
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
	comp, rpcError := a.lookupReadyComponent(args.Root)
	if rpcError != nil {
		return nil, rpcError
	}

	payload := symbolPayload{Results: make([]symbolResult, 0)}
	addWarning := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		a.logger.Warn(msg)
		payload.Warnings = append(payload.Warnings, msg)
	}
	seen := make(map[string]bool)

	for _, sess := range comp.readySessions() {
		lang := sess.Lang
		meta := lsploader.Meta(lang)
		allExcludes := append([]string(nil), meta.DefaultExcludes...)
		allExcludes = append(allExcludes, comp.Excludes...)
		files, err := findSourceFiles(comp.Root, meta.FileExts, allExcludes)
		if err != nil {
			addWarning("language=%s: walk failed: %v", lang, err)
			continue
		}
		for _, file := range files {
			a.collectSymbolsFromFile(ctx, sess, comp.Root, file, args.Query, &payload, seen, addWarning)
		}
	}

	return marshalToolResult(payload)
}

// collectSymbolsFromFile asks sess for the document symbols of file and
// fuzzy-matches each (top-level + nested) symbol against query, appending
// hits to payload.
//
// File availability is the Session's responsibility: the generic
// LiveSession trusts the LSP to read from disk for unopened workspace
// files (gopls / rust-analyzer index everything from initialize), and
// tsSession bulk-opens the project on construction so tsserver has every
// file in its working set. Either way, no per-file didOpen/didClose is
// needed here.
func (a *Adapter) collectSymbolsFromFile(
	ctx context.Context,
	sess *LiveSession,
	root, file, query string,
	payload *symbolPayload,
	seen map[string]bool,
	addWarning func(format string, args ...any),
) {
	uri := fileURIFromPath(file)
	syms, err := sess.DocumentSymbol(ctx, uri)
	if err != nil {
		addWarning("documentSymbol %s: %v", file, err)
		return
	}
	rel, ok := relPathInRoot(root, file)
	if !ok {
		return
	}
	for _, sym := range flattenSymbols(syms) {
		if !fuzzyMatch(query, sym.Name) {
			continue
		}
		id := EncodeSymbolID(SymbolID{
			Lang:    sess.Lang,
			RelPath: rel,
			Line:    sym.SelectionRange.Start.Line,
			Char:    sym.SelectionRange.Start.Character,
			Name:    sym.Name,
		})
		if seen[id] {
			continue
		}
		seen[id] = true
		payload.Results = append(payload.Results, symbolResult{
			ID:        id,
			Name:      sym.Name,
			Kind:      symbolKindName(int(sym.Kind)),
			Path:      rel,
			Line:      sym.SelectionRange.Start.Line,
			Character: sym.SelectionRange.Start.Character,
		})
	}
}

// flattenSymbols returns every symbol in tree (top-level + every nested
// child) as a flat list, preserving traversal order. Hierarchical scope is
// useful for find_references attribution but not for symbol search by name —
// the agent should be able to find a method by its short name even when it
// is nested inside a class.
func flattenSymbols(tree []LSPDocumentSymbol) []LSPDocumentSymbol {
	if len(tree) == 0 {
		return nil
	}
	out := make([]LSPDocumentSymbol, 0, len(tree))
	for _, s := range tree {
		out = append(out, s)
		out = append(out, flattenSymbols(s.Children)...)
	}
	return out
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
	target, err := DecodeSymbolID(args.SymbolID)
	if err != nil {
		return nil, &rpcErr{Code: -32602, Message: err.Error()}
	}
	comp, rpcError := a.lookupReadyComponent(args.Root)
	if rpcError != nil {
		return nil, rpcError
	}
	sess, err := comp.readySession(target.Lang)
	if err != nil {
		return nil, &rpcErr{Code: -32603, Message: err.Error()}
	}

	payload := bfsCallers(ctx, sess, comp, target, a.logger)
	return marshalToolResult(payload)
}

// marshalToolResult JSON-encodes payload and wraps it in the toolCallResult
// content shape.
//
// payload is always one of our internal types (symbolPayload, struct{...})
// composed of string / int / []byte / json.RawMessage / slice / map fields,
// none of which can fail to marshal. A failure here therefore indicates a
// type definition bug — crash loud rather than corrupt the wire protocol
// with empty content.
func marshalToolResult(payload any) (mcpResult, *rpcErr) {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("mcp: marshal tool result (programming bug): %v", err))
	}
	return toolCallResult{Content: []toolCallContent{{Type: "text", Text: string(b)}}}, nil
}

// bfsCallers walks the upstream caller chain from target by repeatedly
// asking the LSP for textDocument/references and resolving each hit to its
// containing symbol via textDocument/documentSymbol.
//
// Symbols outside the component root are ignored (e.g. stdlib, vendored
// dependencies). Excluded paths (per Component.Excludes) are also skipped.
//
// Per-step LSP failures do not abort the walk; they are recorded in
// payload.Warnings so the caller can see that the result is partial. The
// same failures are also logged at Warn for operator visibility.
func bfsCallers(ctx context.Context, sess *LiveSession, comp *Component, target SymbolID, logger *slog.Logger) symbolPayload {
	type queueItem struct {
		uri  string
		line int
		ch   int
	}
	startURI := fileURIFromPath(filepath.Join(comp.Root, target.RelPath))
	queue := []queueItem{{uri: startURI, line: target.Line, ch: target.Char}}
	visited := map[string]bool{
		EncodeSymbolID(target): true,
	}
	payload := symbolPayload{Results: make([]symbolResult, 0)}
	addWarning := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		logger.Warn(msg)
		payload.Warnings = append(payload.Warnings, msg)
	}

	type docCacheEntry struct {
		syms []LSPDocumentSymbol
		err  error
	}
	docCache := make(map[string]docCacheEntry)
	getDoc := func(uri string) []LSPDocumentSymbol {
		if entry, ok := docCache[uri]; ok {
			return entry.syms
		}
		syms, err := sess.DocumentSymbol(ctx, uri)
		docCache[uri] = docCacheEntry{syms: syms, err: err}
		if err != nil {
			// Cache the failure so we don't retry per-reference, but warn
			// once so partial caller attribution is visible.
			addWarning("documentSymbol failed for %s: %v", uri, err)
		}
		return syms
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		locs, err := sess.References(ctx, cur.uri, LSPPosition{Line: cur.line, Character: cur.ch})
		if err != nil {
			addWarning("textDocument/references failed at %s:%d:%d: %v", cur.uri, cur.line, cur.ch, err)
			continue
		}
		for _, loc := range locs {
			refPath := pathFromFileURI(loc.URI)
			rel, ok := relPathInRoot(comp.Root, refPath)
			if !ok {
				continue
			}
			excluded, exErr := isExcluded(rel, comp.Excludes)
			if exErr != nil {
				addWarning("invalid exclude pattern (skipping filter): %v", exErr)
			}
			if excluded {
				continue
			}
			caller := findInnermostSymbol(getDoc(loc.URI), loc.Range.Start)
			if caller == nil {
				continue
			}
			id := EncodeSymbolID(SymbolID{
				Lang:    sess.Lang,
				RelPath: rel,
				Line:    caller.SelectionRange.Start.Line,
				Char:    caller.SelectionRange.Start.Character,
				Name:    caller.Name,
			})
			if visited[id] {
				continue
			}
			visited[id] = true
			payload.Results = append(payload.Results, symbolResult{
				ID:        id,
				Name:      caller.Name,
				Kind:      symbolKindName(int(caller.Kind)),
				Path:      rel,
				Line:      caller.SelectionRange.Start.Line,
				Character: caller.SelectionRange.Start.Character,
			})
			queue = append(queue, queueItem{
				uri:  loc.URI,
				line: caller.SelectionRange.Start.Line,
				ch:   caller.SelectionRange.Start.Character,
			})
		}
	}
	return payload
}

// findInnermostSymbol walks a hierarchical DocumentSymbol tree and returns
// the deepest entry whose Range contains pos, or nil if pos lies outside
// every symbol (e.g. a top-level statement).
func findInnermostSymbol(syms []LSPDocumentSymbol, pos LSPPosition) *LSPDocumentSymbol {
	var best *LSPDocumentSymbol
	for i := range syms {
		if !rangeContains(syms[i].Range, pos) {
			continue
		}
		// Recurse for the innermost match; on no child match, syms[i] wins.
		if child := findInnermostSymbol(syms[i].Children, pos); child != nil {
			best = child
		} else {
			best = &syms[i]
		}
	}
	return best
}

func rangeContains(r LSPRange, p LSPPosition) bool {
	if p.Line < r.Start.Line || (p.Line == r.Start.Line && p.Character < r.Start.Character) {
		return false
	}
	if p.Line > r.End.Line || (p.Line == r.End.Line && p.Character >= r.End.Character) {
		return false
	}
	return true
}

// relPathInRoot returns the path relative to root and reports whether the
// relative form stays within root (i.e. does not start with "..").
func relPathInRoot(root, path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}

// isExcluded reports whether rel matches any of the component's exclude
// globs. Patterns are doublestar-style relative to the component root.
//
// Pattern compilation errors are returned to the caller so the user can be
// alerted that an exclude is being silently ignored — a common source of
// "why is this file in my results" confusion.
func isExcluded(rel string, excludes []string) (bool, error) {
	var firstErr error
	for _, p := range excludes {
		ok, err := doublestar.PathMatch(p, rel)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("pattern %q: %w", p, err)
			}
			continue
		}
		if ok {
			return true, firstErr
		}
	}
	return false, firstErr
}

// symbolKindName maps the LSP SymbolKind enum to the human-readable name
// used by the depgraph domain layer (matches lsp/adapter.go).
func symbolKindName(k int) string {
	switch k {
	case 1:
		return "file"
	case 2:
		return "module"
	case 3:
		return "namespace"
	case 4:
		return "package"
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "property"
	case 8:
		return "field"
	case 9:
		return "constructor"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "function"
	case 13:
		return "variable"
	case 14:
		return "constant"
	case 23:
		return "struct"
	case 26:
		return "typeParameter"
	default:
		return ""
	}
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

// fuzzyMatch is retained for tests and any future client-side filtering;
// the live LSP path delegates query matching to workspace/symbol.
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
