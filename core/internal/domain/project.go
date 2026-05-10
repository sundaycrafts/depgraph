package domain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// IndexState is the lifecycle of a Project's analysis state, derived from
// the union of its per-language sessions.
type IndexState string

const (
	IndexIndexing IndexState = "indexing"
	IndexReady    IndexState = "ready"
	IndexFailed   IndexState = "failed"
)

// Project is a depgraph-registered project root. It owns one
// PortAnalysisSession per detected language plus a PortSourceWatcher for the
// root, and exposes the high-level query methods (FindSymbols,
// FindReferences) the agent-facing tools rely on.
type Project struct {
	Root           string
	Excludes       []string
	ExcludeSymbols []string
	Languages      []lsploader.Language

	watcher PortSourceWatcher
	logger  *slog.Logger

	mu       sync.RWMutex
	sessions map[lsploader.Language]*sessionEntry
}

// sessionEntry tracks a single language session within a Project.
type sessionEntry struct {
	mu      sync.RWMutex
	state   IndexState
	err     error
	session PortAnalysisSession
}

// newProject constructs an empty Project; populated by Workspace.
func newProject(root string, excludes, excludeSymbols []string, langs []lsploader.Language, watcher PortSourceWatcher, logger *slog.Logger) *Project {
	p := &Project{
		Root:           root,
		Excludes:       append([]string(nil), excludes...),
		ExcludeSymbols: append([]string(nil), excludeSymbols...),
		Languages:      append([]lsploader.Language(nil), langs...),
		watcher:        watcher,
		logger:         logger,
		sessions:       make(map[lsploader.Language]*sessionEntry),
	}
	for _, lang := range langs {
		p.sessions[lang] = &sessionEntry{state: IndexIndexing}
	}
	return p
}

// SubscribeFiles returns a channel that receives every source change
// observed under Root. Used by PortAnalysisSession implementations to keep
// in-memory state in sync with disk.
func (p *Project) SubscribeFiles() <-chan SourceChange {
	if p.watcher == nil {
		ch := make(chan SourceChange)
		close(ch)
		return ch
	}
	return p.watcher.Subscribe()
}

// State returns the aggregate IndexState across every language session.
// All-Ready → Ready; any-Failed → Failed (with the first error); else
// Indexing.
func (p *Project) State() (IndexState, error) {
	entries := p.snapshotEntries()
	if len(entries) == 0 {
		return IndexIndexing, nil
	}
	ready := 0
	var firstErr error
	for _, e := range entries {
		e.mu.RLock()
		st := e.state
		err := e.err
		e.mu.RUnlock()
		switch st {
		case IndexFailed:
			if firstErr == nil {
				firstErr = err
			}
		case IndexReady:
			ready++
		}
	}
	switch {
	case firstErr != nil:
		return IndexFailed, firstErr
	case ready == len(entries):
		return IndexReady, nil
	default:
		return IndexIndexing, nil
	}
}

// Shutdown stops the source watcher (so subscriber goroutines drain
// before LSP shutdown) and then tears down every session in parallel.
func (p *Project) Shutdown() {
	if p.watcher != nil {
		p.watcher.Stop()
	}
	var wg sync.WaitGroup
	for _, entry := range p.snapshotEntries() {
		entry.mu.Lock()
		sess := entry.session
		entry.session = nil
		entry.state = IndexFailed
		entry.err = errors.New("shutdown")
		entry.mu.Unlock()
		if sess == nil {
			continue
		}
		wg.Add(1)
		go func(s PortAnalysisSession) {
			defer wg.Done()
			s.Shutdown()
		}(sess)
	}
	wg.Wait()
}

// FindSymbols walks the project, asks each ready session for the symbols
// of every source file, and fuzzy-matches against query. Errors are
// collected as warnings so partial results stay visible.
//
// Returns IndexFailed / IndexIndexing as an error if any language is not
// ready (the caller surfaces this to the agent as "retry shortly").
func (p *Project) FindSymbols(ctx context.Context, query string) (PartialResult[Symbol], error) {
	if err := p.ensureReady(); err != nil {
		return PartialResult[Symbol]{}, err
	}

	payload := PartialResult[Symbol]{Results: make([]Symbol, 0)}
	addWarning := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		p.logger.Warn(msg)
		payload.AppendWarning(msg)
	}
	seen := make(map[string]bool)

	for _, sess := range p.readySessions() {
		lang := sess.Lang()
		meta := lsploader.Meta(lang)
		allExcludes := append([]string(nil), meta.DefaultExcludes...)
		allExcludes = append(allExcludes, p.Excludes...)
		files, err := WalkSourceFiles(p.Root, meta.FileExts, allExcludes)
		if err != nil {
			addWarning("language=%s: walk failed: %v", lang, err)
			continue
		}
		for _, file := range files {
			p.collectSymbolsFromFile(ctx, sess, file, query, &payload, seen, addWarning)
		}
	}
	return payload, nil
}

// FindReferences walks the upstream caller chain from target by
// repeatedly asking the LSP for textDocument/references and resolving
// each hit to its containing symbol via documentSymbol.
//
// Per-step LSP failures do not abort the walk; they are logged and
// recorded in the returned warnings so the caller knows the result is
// partial.
func (p *Project) FindReferences(ctx context.Context, target SymbolID) (PartialResult[Symbol], error) {
	if err := p.ensureReady(); err != nil {
		return PartialResult[Symbol]{}, err
	}
	sess, err := p.readySession(target.Lang)
	if err != nil {
		return PartialResult[Symbol]{}, err
	}

	// Combine the language's default excludes (vendor/, target/,
	// node_modules/, …) with the user's. Without this the BFS would
	// happily recurse into upstream library code — a single import of a
	// large npm package could fan out into thousands of opaque symbols
	// the user cannot edit.
	meta := lsploader.Meta(sess.Lang())
	allExcludes := make([]string, 0, len(meta.DefaultExcludes)+len(p.Excludes))
	allExcludes = append(allExcludes, meta.DefaultExcludes...)
	allExcludes = append(allExcludes, p.Excludes...)

	type queueItem struct {
		uri  string
		line int
		ch   int
	}
	startURI := FileURIFromPath(filepath.Join(p.Root, target.RelPath))
	queue := []queueItem{{uri: startURI, line: target.Line, ch: target.Char}}
	visited := map[string]bool{
		EncodeSymbolID(target): true,
	}
	payload := PartialResult[Symbol]{Results: make([]Symbol, 0)}
	addWarning := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		p.logger.Warn(msg)
		payload.AppendWarning(msg)
	}

	type docCacheEntry struct {
		syms []DocumentSymbol
		err  error
	}
	docCache := make(map[string]docCacheEntry)
	getDoc := func(uri string) []DocumentSymbol {
		if entry, ok := docCache[uri]; ok {
			return entry.syms
		}
		syms, err := sess.DocumentSymbol(ctx, uri)
		docCache[uri] = docCacheEntry{syms: syms, err: err}
		if err != nil {
			addWarning("documentSymbol failed for %s: %v", uri, err)
		}
		return syms
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		locs, err := sess.References(ctx, cur.uri, Position{Line: cur.line, Character: cur.ch})
		if err != nil {
			addWarning("textDocument/references failed at %s:%d:%d: %v", cur.uri, cur.line, cur.ch, err)
			continue
		}
		for _, loc := range locs {
			refPath := PathFromFileURI(loc.URI)
			rel, ok := RelPathInRoot(p.Root, refPath)
			if !ok {
				continue
			}
			excluded, exErr := IsExcluded(rel, allExcludes)
			if exErr != nil {
				addWarning("invalid exclude pattern (skipping filter): %v", exErr)
			}
			if excluded {
				continue
			}
			caller := InnermostSymbol(getDoc(loc.URI), loc.Range.Start)
			if caller == nil {
				continue
			}
			symExcluded, symErr := IsSymbolExcluded(caller.Name, caller.Kind, p.ExcludeSymbols)
			if symErr != nil {
				addWarning("invalid exclude_symbol pattern (skipping filter): %v", symErr)
			}
			if symExcluded {
				continue
			}
			id := EncodeSymbolID(SymbolID{
				Lang:    sess.Lang(),
				RelPath: rel,
				Line:    caller.SelectionRange.Start.Line,
				Char:    caller.SelectionRange.Start.Character,
				Name:    caller.Name,
			})
			if visited[id] {
				continue
			}
			visited[id] = true
			payload.AppendResult(Symbol{
				ID:        id,
				Name:      caller.Name,
				Kind:      caller.Kind,
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
	return payload, nil
}

// collectSymbolsFromFile asks sess for the document symbols of file and
// fuzzy-matches each (top-level + nested) symbol against query, appending
// hits to payload.
func (p *Project) collectSymbolsFromFile(
	ctx context.Context,
	sess PortAnalysisSession,
	file, query string,
	payload *PartialResult[Symbol],
	seen map[string]bool,
	addWarning func(format string, args ...any),
) {
	uri := FileURIFromPath(file)
	syms, err := sess.DocumentSymbol(ctx, uri)
	if err != nil {
		addWarning("documentSymbol %s: %v", file, err)
		return
	}
	rel, ok := RelPathInRoot(p.Root, file)
	if !ok {
		return
	}
	for _, sym := range FlattenDocumentSymbols(syms) {
		if !FuzzyMatch(query, sym.Name) {
			continue
		}
		id := EncodeSymbolID(SymbolID{
			Lang:    sess.Lang(),
			RelPath: rel,
			Line:    sym.SelectionRange.Start.Line,
			Char:    sym.SelectionRange.Start.Character,
			Name:    sym.Name,
		})
		if seen[id] {
			continue
		}
		seen[id] = true
		payload.AppendResult(Symbol{
			ID:        id,
			Name:      sym.Name,
			Kind:      sym.Kind,
			Path:      rel,
			Line:      sym.SelectionRange.Start.Line,
			Character: sym.SelectionRange.Start.Character,
		})
	}
}

// ensureReady returns an error appropriate for surfacing to the agent
// when State() is not Ready.
func (p *Project) ensureReady() error {
	state, err := p.State()
	switch state {
	case IndexReady:
		return nil
	case IndexFailed:
		return fmt.Errorf("project failed: %w", err)
	default:
		return errors.New("project is indexing, retry shortly")
	}
}

// readySession returns the session for lang if it is in Ready state.
func (p *Project) readySession(lang lsploader.Language) (PortAnalysisSession, error) {
	p.mu.RLock()
	entry := p.sessions[lang]
	p.mu.RUnlock()
	if entry == nil {
		return nil, fmt.Errorf("no session for language %s", lang)
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	switch entry.state {
	case IndexReady:
		return entry.session, nil
	case IndexFailed:
		return nil, fmt.Errorf("session for %s failed: %w", lang, entry.err)
	default:
		return nil, fmt.Errorf("session for %s is still indexing", lang)
	}
}

// readySessions returns every language whose session is in Ready state.
func (p *Project) readySessions() []PortAnalysisSession {
	entries := p.snapshotEntries()
	out := make([]PortAnalysisSession, 0, len(entries))
	for _, e := range entries {
		e.mu.RLock()
		if e.state == IndexReady && e.session != nil {
			out = append(out, e.session)
		}
		e.mu.RUnlock()
	}
	return out
}

func (p *Project) snapshotEntries() map[lsploader.Language]*sessionEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[lsploader.Language]*sessionEntry, len(p.sessions))
	for k, v := range p.sessions {
		out[k] = v
	}
	return out
}

// markReady installs sess as the ready session for lang. Used by the
// Workspace's launchSession goroutine.
func (p *Project) markReady(lang lsploader.Language, sess PortAnalysisSession) {
	p.mu.RLock()
	entry := p.sessions[lang]
	p.mu.RUnlock()
	if entry == nil {
		sess.Shutdown()
		return
	}
	entry.mu.Lock()
	entry.state = IndexReady
	entry.session = sess
	entry.err = nil
	entry.mu.Unlock()
}

// markFailed records a failure for lang's session.
func (p *Project) markFailed(lang lsploader.Language, err error) {
	p.logger.Error("session failed", "root", p.Root, "lang", string(lang), "err", err)
	p.mu.RLock()
	entry := p.sessions[lang]
	p.mu.RUnlock()
	if entry == nil {
		return
	}
	entry.mu.Lock()
	entry.state = IndexFailed
	entry.err = err
	entry.session = nil
	entry.mu.Unlock()
}
