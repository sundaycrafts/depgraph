package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// liveSession owns a long-lived language-server subprocess and implements
// domain.PortAnalysisSession. One liveSession corresponds to a single
// (project root, language) pair.
//
// For tsserver-based servers the construction also opens every project
// file in the LSP's working set (tsserver only resolves cross-file refs
// for files in its open set) and starts a goroutine that translates
// SourceChange events into didChange / didOpen / didClose so the LSP's
// in-memory view tracks disk.
type liveSession struct {
	lang   lsploader.Language
	root   string
	cmd    *exec.Cmd
	conn   *conn
	logger *slog.Logger

	stderrTail *stderrTail
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

var _ domain.PortAnalysisSession = (*liveSession)(nil)

// startLiveSession launches the LSP defined by spec for root, performs
// the initialize handshake, waits for initial indexing to settle, and
// returns the ready-to-use session. See package-level docs and
// domain.PortAnalysisSessionFactory for details.
func startLiveSession(
	ctx context.Context,
	lang lsploader.Language,
	root string,
	excludes []string,
	spec LSPConfig,
	events <-chan domain.SourceChange,
	logger *slog.Logger,
) (*liveSession, error) {
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

	tail := newStderrTail(50)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			tail.add(line)
			logger.Debug("lsp stderr", "line", line)
		}
	}()

	c := newConn(stdout, stdin, logger)
	go func() {
		if err := c.readLoop(); err != nil {
			logger.Error("LSP read loop exited with error", "err", err)
		} else {
			logger.Debug("LSP read loop exited (EOF)")
		}
	}()

	rootURI := fileURI(root)
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

	sess := &liveSession{
		lang:       lang,
		root:       root,
		cmd:        cmd,
		conn:       c,
		logger:     logger,
		stderrTail: tail,
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

	c.waitForIdle(ctx)
	return sess, nil
}

// preloadProject walks root and bulk-opens every source file matching
// the language's extensions (combined with the user-supplied excludes).
// Only invoked for TypeScript. tsserver requires this priming before
// workspace-wide queries return cross-file results, and per-call
// open/close around BFS would thrash the project state.
func (s *liveSession) preloadProject(excludes []string) error {
	meta := lsploader.Meta(s.lang)
	allExcludes := make([]string, 0, len(meta.DefaultExcludes)+len(excludes))
	allExcludes = append(allExcludes, meta.DefaultExcludes...)
	allExcludes = append(allExcludes, excludes...)

	files, err := domain.WalkSourceFiles(s.root, meta.FileExts, allExcludes)
	if err != nil {
		return fmt.Errorf("walk %s: %w", s.root, err)
	}
	for _, file := range files {
		text, rerr := os.ReadFile(file)
		if rerr != nil {
			s.logger.Warn("preload: read failed", "file", file, "err", rerr)
			continue
		}
		uri := fileURI(file)
		if err := s.didOpen(uri, langIDForFile(s.lang, file), string(text)); err != nil {
			s.logger.Warn("preload: didOpen failed", "file", file, "err", err)
			continue
		}
	}
	s.logger.Info("preload: project loaded into open set", "files", len(s.openedURIs))
	return nil
}

// runEventLoop translates SourceChange events into LSP document-sync
// notifications. Returns when events is closed (the watcher's Stop fires
// that close), so cancellation is implicit.
func (s *liveSession) runEventLoop(events <-chan domain.SourceChange, excludes []string) {
	meta := lsploader.Meta(s.lang)
	allExcludes := make([]string, 0, len(meta.DefaultExcludes)+len(excludes))
	allExcludes = append(allExcludes, meta.DefaultExcludes...)
	allExcludes = append(allExcludes, excludes...)

	matches := func(path string) bool {
		if !domain.MatchesAnyExt(path, meta.FileExts) {
			return false
		}
		rel, err := filepath.Rel(s.root, path)
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
		uri := fileURI(ev.Path)
		switch ev.Op {
		case domain.FileCreated, domain.FileModified:
			text, err := os.ReadFile(ev.Path)
			if err != nil {
				s.logger.Warn("event: read failed", "file", ev.Path, "err", err)
				continue
			}
			s.docMu.Lock()
			alreadyOpen := s.openedURIs[uri]
			s.docMu.Unlock()
			if alreadyOpen {
				if err := s.didChange(uri, string(text)); err != nil {
					s.logger.Warn("event: didChange failed", "file", ev.Path, "err", err)
				}
			} else {
				if err := s.didOpen(uri, langIDForFile(s.lang, ev.Path), string(text)); err != nil {
					s.logger.Warn("event: didOpen failed", "file", ev.Path, "err", err)
				}
			}
		case domain.FileDeleted:
			s.docMu.Lock()
			isOpen := s.openedURIs[uri]
			s.docMu.Unlock()
			if !isOpen {
				continue
			}
			if err := s.didClose(uri); err != nil {
				s.logger.Warn("event: didClose failed", "file", ev.Path, "err", err)
			}
		}
	}
}

// Lang reports the language this session was started for.
func (s *liveSession) Lang() lsploader.Language { return s.lang }

// DocumentSymbol queries hierarchical symbols for a single document.
// Returns nil if the document has no symbols or the server returns null.
func (s *liveSession) DocumentSymbol(ctx context.Context, uri string) ([]domain.DocumentSymbol, error) {
	params := DocumentSymbolParams{TextDocument: TextDocumentIdentifier{URI: uri}}
	var raw json.RawMessage
	if err := s.conn.call(ctx, "textDocument/documentSymbol", params, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var lspSyms []DocumentSymbol
	if err := json.Unmarshal(raw, &lspSyms); err != nil {
		return nil, fmt.Errorf("decode documentSymbol: %w", err)
	}
	return toDomainDocumentSymbols(lspSyms), nil
}

// References issues textDocument/references for the symbol at (uri, pos).
func (s *liveSession) References(ctx context.Context, uri string, pos domain.Position) ([]domain.ReferenceLocation, error) {
	params := ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: pos.Line, Character: pos.Character},
		Context:      ReferenceContext{IncludeDeclaration: false},
	}
	var locs []Location
	if err := s.conn.call(ctx, "textDocument/references", params, &locs); err != nil {
		return nil, err
	}
	out := make([]domain.ReferenceLocation, 0, len(locs))
	for _, l := range locs {
		out = append(out, domain.ReferenceLocation{
			URI:   l.URI,
			Range: toDomainRange(l.Range),
		})
	}
	return out, nil
}

// Shutdown closes any URIs that are currently in the open set, sends LSP
// shutdown/exit, waits up to 5s, then SIGKILLs and reaps. Safe to call
// multiple times.
func (s *liveSession) Shutdown() {
	s.docMu.Lock()
	uris := make([]string, 0, len(s.openedURIs))
	for uri := range s.openedURIs {
		uris = append(uris, uri)
	}
	s.docMu.Unlock()
	for _, uri := range uris {
		if err := s.didClose(uri); err != nil {
			s.logger.Debug("didClose during shutdown failed", "uri", uri, "err", err)
		}
	}
	shutdownLSP(s.cmd, s.conn, s.stderrTail, s.stderrDone, s.logger)
}

// didOpen sends textDocument/didOpen, registering uri in the LSP's open
// set with version 1.
func (s *liveSession) didOpen(uri, languageID, text string) error {
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

// didChange sends textDocument/didChange with a full-text replacement.
// Version is monotonically incremented per URI so the LSP can detect
// out-of-order updates.
func (s *liveSession) didChange(uri, text string) error {
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

// didClose sends textDocument/didClose and removes uri from our open set.
func (s *liveSession) didClose(uri string) error {
	s.docMu.Lock()
	delete(s.openedURIs, uri)
	delete(s.versions, uri)
	s.docMu.Unlock()
	return s.conn.notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

// toDomainDocumentSymbols maps the LSP wire form into the domain form.
func toDomainDocumentSymbols(in []DocumentSymbol) []domain.DocumentSymbol {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.DocumentSymbol, 0, len(in))
	for _, s := range in {
		out = append(out, domain.DocumentSymbol{
			Name:           s.Name,
			Kind:           domain.SymbolKindFromLSP(int(s.Kind)),
			Range:          toDomainRange(s.Range),
			SelectionRange: toDomainRange(s.SelectionRange),
			Children:       toDomainDocumentSymbols(s.Children),
		})
	}
	return out
}

