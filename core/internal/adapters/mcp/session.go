package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// errLSPConnClosed is returned when the LSP read loop exits before a
// pending request is answered (e.g. the language server died).
var errLSPConnClosed = errors.New("LSP connection closed")

// LiveSession owns a long-lived language server subprocess and exposes the
// LSP operations the MCP tools need. One LiveSession corresponds to a
// single (component root, language) pair.
type LiveSession struct {
	Lang lsploader.Language
	Root string

	cmd    *exec.Cmd
	conn   *lspConn
	logger *slog.Logger

	stderrDone chan struct{}

	// docMu guards openedURIs and versions. They are tracked together
	// because every didOpen / didChange / didClose mutates both.
	docMu sync.Mutex
	// openedURIs records URIs currently in the LSP's open set. Populated
	// during preload (TypeScript only) and on incoming Create / Modify
	// events from the file watcher; entries removed on didClose.
	openedURIs map[string]bool
	// versions tracks the textDocument version we last sent the LSP for
	// each URI. didOpen sets it to 1; didChange increments before send.
	versions map[string]int64
}

// startLiveSession launches the LSP defined by spec for root, performs the
// initialize handshake, waits for initial indexing to settle, and returns
// the ready-to-use session.
//
// For TypeScript components the construction also opens every project file
// in tsserver's working set and starts a goroutine that translates events
// from the supplied channel into didChange / didOpen / didClose so the
// LSP's view stays in sync with disk. tsserver's references query only
// surfaces refs from files in the open set, and per-call open/close would
// make BFS thrash the project state. Encapsulating this here keeps the
// call sites (find_symbols / find_references) language-agnostic.
//
// `events` is the file-event channel the session subscribes to. It is only
// consumed for TypeScript; other languages receive an unread channel that
// the caller is free to leave dangling. Pass nil to skip subscription
// entirely (used by tests).
//
// Failures (LSP startup, initialize handshake, preload walk) tear down the
// subprocess before returning.
func startLiveSession(
	ctx context.Context,
	lang lsploader.Language,
	root string,
	excludes []string,
	spec LSPConfig,
	events <-chan FileEvent,
	logger *slog.Logger,
) (*LiveSession, error) {
	logger = logger.With("lang", string(lang), "root", root, "lsp", spec.Command)

	cmd := exec.Command(spec.Command, spec.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.Command, err)
	}

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			logger.Debug("lsp stderr", "line", sc.Text())
		}
	}()

	c := newLSPConn(stdout, stdin, logger)
	go func() {
		if err := c.readLoop(); err != nil {
			logger.Error("LSP read loop exited", "err", err)
		}
	}()

	rootURI := fileURIFromPath(root)
	initParams := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"documentSymbol": map[string]any{
					"hierarchicalDocumentSymbolSupport": true,
				},
			},
			"workspace": map[string]any{
				"symbol": map[string]any{},
			},
			"window": map[string]any{
				"workDoneProgress": true,
			},
		},
	}
	if len(spec.InitializationOptions) > 0 && string(spec.InitializationOptions) != "null" {
		initParams["initializationOptions"] = spec.InitializationOptions
	}

	if err := c.call(ctx, "initialize", initParams, nil); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("initialized notification: %w", err)
	}

	sess := &LiveSession{
		Lang:       lang,
		Root:       root,
		cmd:        cmd,
		conn:       c,
		logger:     logger,
		stderrDone: stderrDone,
		openedURIs: make(map[string]bool),
		versions:   make(map[string]int64),
	}

	if lang == lsploader.TypeScript {
		if err := sess.preloadProject(excludes); err != nil {
			sess.Shutdown()
			return nil, fmt.Errorf("preload typescript project: %w", err)
		}
		if events != nil {
			go sess.runEventLoop(events, excludes)
		}
	}

	sess.conn.waitForIdle(ctx)
	return sess, nil
}

// preloadProject walks Root and bulk-opens every source file matching the
// language's extensions (combined with the user-supplied excludes). It is
// only invoked for TypeScript components — tsserver requires this priming
// before workspace-wide queries return cross-file results, and per-call
// open/close around BFS would thrash the project state.
//
// Per-file failures (read errors, didOpen RPC errors) are logged at Warn
// and skipped: preload is best-effort and the rest of the project still
// participates in queries.
func (s *LiveSession) preloadProject(excludes []string) error {
	meta := lsploader.Meta(s.Lang)
	allExcludes := make([]string, 0, len(meta.DefaultExcludes)+len(excludes))
	allExcludes = append(allExcludes, meta.DefaultExcludes...)
	allExcludes = append(allExcludes, excludes...)

	files, err := findSourceFiles(s.Root, meta.FileExts, allExcludes)
	if err != nil {
		return fmt.Errorf("walk %s: %w", s.Root, err)
	}
	for _, file := range files {
		text, rerr := os.ReadFile(file)
		if rerr != nil {
			s.logger.Warn("preload: read failed", "file", file, "err", rerr)
			continue
		}
		uri := fileURIFromPath(file)
		if err := s.DidOpen(uri, langIDForFile(s.Lang, file), string(text)); err != nil {
			s.logger.Warn("preload: didOpen failed", "file", file, "err", err)
			continue
		}
	}
	s.logger.Info("preload: project loaded into open set", "files", len(s.openedURIs))
	return nil
}

// runEventLoop translates file watcher events into LSP document-sync
// notifications. It returns when events is closed (the watcher's Stop
// fires that close), so cancellation is implicit.
//
// Errors during read / RPC are logged at Warn — a transient failure leaves
// the LSP slightly out of date until the next event for the same path.
func (s *LiveSession) runEventLoop(events <-chan FileEvent, excludes []string) {
	meta := lsploader.Meta(s.Lang)
	allExcludes := make([]string, 0, len(meta.DefaultExcludes)+len(excludes))
	allExcludes = append(allExcludes, meta.DefaultExcludes...)
	allExcludes = append(allExcludes, excludes...)

	matches := func(path string) bool {
		extOK := false
		for _, ext := range meta.FileExts {
			if strings.HasSuffix(path, ext) {
				extOK = true
				break
			}
		}
		if !extOK {
			return false
		}
		rel, err := filepath.Rel(s.Root, path)
		if err != nil {
			return false
		}
		for _, p := range allExcludes {
			if ok, mErr := doublestar.PathMatch(p, rel); mErr == nil && ok {
				return false
			}
		}
		return true
	}

	for ev := range events {
		if !matches(ev.Path) {
			continue
		}
		uri := fileURIFromPath(ev.Path)
		switch ev.Op {
		case FileCreated, FileModified:
			text, err := os.ReadFile(ev.Path)
			if err != nil {
				s.logger.Warn("event: read failed", "file", ev.Path, "err", err)
				continue
			}
			s.docMu.Lock()
			alreadyOpen := s.openedURIs[uri]
			s.docMu.Unlock()
			if alreadyOpen {
				if err := s.DidChange(uri, string(text)); err != nil {
					s.logger.Warn("event: didChange failed", "file", ev.Path, "err", err)
				}
			} else {
				if err := s.DidOpen(uri, langIDForFile(s.Lang, ev.Path), string(text)); err != nil {
					s.logger.Warn("event: didOpen failed", "file", ev.Path, "err", err)
				}
			}
		case FileDeleted:
			s.docMu.Lock()
			isOpen := s.openedURIs[uri]
			s.docMu.Unlock()
			if !isOpen {
				continue
			}
			if err := s.DidClose(uri); err != nil {
				s.logger.Warn("event: didClose failed", "file", ev.Path, "err", err)
			}
		}
	}
}

// Shutdown closes any URIs that are currently in the open set, sends LSP
// shutdown/exit, waits up to 5s, then SIGKILLs and reaps.
//
// The didClose, shutdown, and exit RPCs may legitimately fail (the server
// has crashed, the pipe is already half-closed, etc.); those are logged at
// Debug because they are noise in the normal-shutdown case but useful when
// investigating a language server that hangs.
func (s *LiveSession) Shutdown() {
	s.docMu.Lock()
	uris := make([]string, 0, len(s.openedURIs))
	for uri := range s.openedURIs {
		uris = append(uris, uri)
	}
	s.docMu.Unlock()
	for _, uri := range uris {
		if err := s.DidClose(uri); err != nil {
			s.logger.Debug("didClose during shutdown failed", "uri", uri, "err", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := s.conn.call(shutdownCtx, "shutdown", nil, nil); err != nil {
		s.logger.Debug("LSP shutdown call failed", "err", err)
	}
	cancel()
	if err := s.conn.notify("exit", map[string]any{}); err != nil {
		s.logger.Debug("LSP exit notification failed", "err", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- s.cmd.Wait() }()

	select {
	case <-waitCh:
	case <-time.After(5 * time.Second):
		if err := s.cmd.Process.Kill(); err != nil {
			s.logger.Warn("LSP process kill failed", "err", err)
		}
		<-waitCh
		s.logger.Warn("LSP killed after shutdown timeout")
	}

	select {
	case <-s.stderrDone:
	case <-time.After(time.Second):
	}
}

// LSPSymbolKind mirrors the SymbolKind enum from the LSP spec.
type LSPSymbolKind int

// LSPPosition is an LSP position (0-based line, UTF-16 character offset).
type LSPPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LSPRange is an LSP range.
type LSPRange struct {
	Start LSPPosition `json:"start"`
	End   LSPPosition `json:"end"`
}

// LSPLocation is a file URI plus range.
type LSPLocation struct {
	URI   string   `json:"uri"`
	Range LSPRange `json:"range"`
}

// LSPDocumentSymbol is a hierarchical symbol returned by
// textDocument/documentSymbol.
type LSPDocumentSymbol struct {
	Name           string              `json:"name"`
	Kind           LSPSymbolKind       `json:"kind"`
	Range          LSPRange            `json:"range"`
	SelectionRange LSPRange            `json:"selectionRange"`
	Children       []LSPDocumentSymbol `json:"children,omitempty"`
}

// References issues textDocument/references for the symbol at (uri, pos).
func (s *LiveSession) References(ctx context.Context, uri string, pos LSPPosition) ([]LSPLocation, error) {
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
		"context":      map[string]any{"includeDeclaration": false},
	}
	var locs []LSPLocation
	if err := s.conn.call(ctx, "textDocument/references", params, &locs); err != nil {
		return nil, err
	}
	return locs, nil
}

// DocumentSymbol queries hierarchical symbols for a single document.
// Returns nil if the document has no symbols or the server returns null.
func (s *LiveSession) DocumentSymbol(ctx context.Context, uri string) ([]LSPDocumentSymbol, error) {
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}
	var raw json.RawMessage
	if err := s.conn.call(ctx, "textDocument/documentSymbol", params, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var docSyms []LSPDocumentSymbol
	if err := json.Unmarshal(raw, &docSyms); err == nil && len(docSyms) > 0 {
		return docSyms, nil
	}
	return nil, nil
}

// DidOpen sends textDocument/didOpen, registering the URI in the LSP's
// open set with version 1. Some servers (notably typescript-language-server)
// require this before they will answer queries for a file.
//
// If the URI is already open, version is reset to 1; the caller is
// responsible for tracking that case (preload only opens unknown URIs;
// the watcher event loop branches into DidChange when it has been opened
// before).
func (s *LiveSession) DidOpen(uri, languageID, text string) error {
	s.docMu.Lock()
	s.openedURIs[uri] = true
	s.versions[uri] = 1
	s.docMu.Unlock()
	return s.conn.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	})
}

// DidChange sends textDocument/didChange with a full-text replacement
// (TextDocumentSyncKind: Full). The version is monotonically incremented
// per URI so the LSP can detect out-of-order updates.
func (s *LiveSession) DidChange(uri, text string) error {
	s.docMu.Lock()
	s.versions[uri]++
	v := s.versions[uri]
	s.docMu.Unlock()
	return s.conn.notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{
			"uri":     uri,
			"version": v,
		},
		"contentChanges": []map[string]any{
			{"text": text},
		},
	})
}

// DidClose sends textDocument/didClose, releasing any working-set state the
// server kept for the document and removing it from our open set tracking.
func (s *LiveSession) DidClose(uri string) error {
	s.docMu.Lock()
	delete(s.openedURIs, uri)
	delete(s.versions, uri)
	s.docMu.Unlock()
	return s.conn.notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

// findSourceFiles walks root collecting files whose extension matches any
// of exts. Dot entries (other than root itself) are always skipped. The
// excludes slice contains doublestar glob patterns matched against paths
// relative to root; matches skip the file or directory.
//
// Invalid glob patterns are returned as the first error. Filesystem errors
// during the walk also propagate.
func findSourceFiles(root string, exts, excludes []string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		for _, p := range excludes {
			ok, mErr := doublestar.PathMatch(p, rel)
			if mErr != nil {
				return fmt.Errorf("invalid exclude pattern %q: %w", p, mErr)
			}
			if ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		for _, ext := range exts {
			if strings.HasSuffix(path, ext) {
				files = append(files, path)
				break
			}
		}
		return nil
	})
	return files, err
}

// langIDForFile returns the LSP languageId field used in textDocument/didOpen
// for the given file in lang. The values mirror lsp/adapter.go so tsserver
// classifies .tsx as typescriptreact correctly.
func langIDForFile(lang lsploader.Language, path string) string {
	switch lang {
	case lsploader.Go:
		return "go"
	case lsploader.Rust:
		return "rust"
	case lsploader.TypeScript:
		if strings.HasSuffix(path, ".tsx") {
			return "typescriptreact"
		}
		return "typescript"
	default:
		return ""
	}
}

// fileURIFromPath converts an absolute path to a file:// URI.
func fileURIFromPath(p string) string {
	u := &url.URL{Scheme: "file", Path: p}
	return u.String()
}

// pathFromFileURI is the inverse of fileURIFromPath.
func pathFromFileURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return u.Path
}

// --- LSP JSON-RPC wire layer ----------------------------------------------
//
// This is a slimmed-down peer of core/internal/adapters/lsp/rpc.go, kept
// separate so the HTTP one-shot Analyze() path can keep its own client.

type lspConn struct {
	w       io.Writer
	br      *bufio.Reader
	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan lspRPCMessage
	pendMu  sync.Mutex

	progMu         sync.Mutex
	progFlight     int
	progLastChange time.Time
	progBeganOnce  sync.Once
	progBeganCh    chan struct{}

	done chan struct{}

	logger *slog.Logger
}

type lspRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *lspRPCError    `json:"error,omitempty"`
}

type lspRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *lspRPCError) Error() string {
	return fmt.Sprintf("lsp rpc error %d: %s", e.Code, e.Message)
}

func newLSPConn(r io.Reader, w io.Writer, logger *slog.Logger) *lspConn {
	return &lspConn{
		w:           w,
		br:          bufio.NewReader(r),
		pending:     make(map[int64]chan lspRPCMessage),
		progBeganCh: make(chan struct{}),
		done:        make(chan struct{}),
		logger:      logger,
	}
}

func (c *lspConn) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)

	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	ch := make(chan lspRPCMessage, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	cleanup := func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}

	if err := c.send(&lspRPCMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  rawParams,
	}); err != nil {
		cleanup()
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-ctx.Done():
		cleanup()
		return ctx.Err()
	case <-c.done:
		cleanup()
		return errLSPConnClosed
	}
}

func (c *lspConn) notify(method string, params any) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	return c.send(&lspRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
	})
}

func (c *lspConn) send(msg *lspRPCMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return err
}

func (c *lspConn) readMessage() ([]byte, error) {
	contentLen := -1
	for {
		line, err := c.br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("LSP: invalid Content-Length: %w", err)
			}
			contentLen = n
		}
	}
	if contentLen < 0 {
		return nil, fmt.Errorf("LSP: missing Content-Length header")
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(c.br, body); err != nil {
		return nil, fmt.Errorf("LSP: read body: %w", err)
	}
	return body, nil
}

func (c *lspConn) readLoop() error {
	defer close(c.done)
	for {
		body, err := c.readMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var msg lspRPCMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			c.logger.Warn("LSP sent malformed JSON-RPC frame", "err", err, "body", string(body))
			continue
		}
		switch {
		case msg.ID != nil && msg.Method != "":
			// Server-initiated request — ack the ones we know about so the
			// server can proceed (mirrors the lsp.adapter behaviour).
			if msg.Method == "window/workDoneProgress/create" {
				if err := c.send(&lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage("null")}); err != nil {
					// Failure here means the server cannot start its progress
					// channels; waitForIdle will fall back to its 30s startup
					// budget and the indexer may stall silently.
					c.logger.Warn("failed to ack window/workDoneProgress/create", "err", err)
				}
			}
		case msg.ID != nil:
			c.pendMu.Lock()
			ch, ok := c.pending[*msg.ID]
			if ok {
				delete(c.pending, *msg.ID)
			}
			c.pendMu.Unlock()
			if ok {
				ch <- msg
			}
		case msg.Method == "$/progress":
			c.handleProgress(msg.Params)
		}
	}
}

func (c *lspConn) handleProgress(params json.RawMessage) {
	var p struct {
		Value struct {
			Kind string `json:"kind"`
		} `json:"value"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		// A malformed $/progress frame breaks our begin/end accounting in
		// waitForIdle: phaseFlight stays out of sync and the session may
		// either declare "ready" too early or wait the full 30s startup
		// budget. Worth surfacing so investigators have a hint.
		c.logger.Warn("malformed $/progress payload", "err", err, "params", string(params))
		return
	}
	var signalBegin bool
	c.progMu.Lock()
	switch p.Value.Kind {
	case "begin":
		c.progFlight++
		signalBegin = true
	case "end":
		if c.progFlight > 0 {
			c.progFlight--
		}
	}
	c.progLastChange = time.Now()
	c.progMu.Unlock()
	if signalBegin {
		c.progBeganOnce.Do(func() { close(c.progBeganCh) })
	}
}

// waitForIdle mirrors lsp/rpc.go: wait for the first $/progress begin (with
// a startup window), then wait until progFlight has stayed at 0 for a quiet
// period. Phased indexing (rust-analyzer) briefly hits zero between phases,
// so a simple "first time at zero" check declares idle prematurely.
//
// The startup window is intentionally short (5s). gopls and rust-analyzer
// emit their first $/progress begin within hundreds of milliseconds of the
// initialized notification — anything beyond that is a server that does
// not advertise progress at all (notably tsserver, whose project graph is
// only built on demand by didOpen). Waiting longer only delays the agent's
// first usable Ready transition.
func (c *lspConn) waitForIdle(ctx context.Context) {
	const maxStartupWait = 5 * time.Second
	const quietPeriod = 2 * time.Second
	const poll = 200 * time.Millisecond

	select {
	case <-c.progBeganCh:
		c.logger.Info("indexing started")
	case <-time.After(maxStartupWait):
		c.logger.Debug("no indexing activity detected, assuming server is ready")
		return
	case <-ctx.Done():
		return
	}

	for {
		c.progMu.Lock()
		flight := c.progFlight
		lastChange := c.progLastChange
		c.progMu.Unlock()
		if flight == 0 && time.Since(lastChange) >= quietPeriod {
			c.logger.Info("indexing complete")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}
