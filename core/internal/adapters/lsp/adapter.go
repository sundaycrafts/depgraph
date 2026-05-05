package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
	"github.com/sundaycrafts/depgraph/internal/ports"
)

// progressInterval is how often (in files) Pass 1/2 emit a Debug progress log.
const progressInterval = 100

// Adapter implements ports.AnalyzerPort via the Language Server Protocol.
// It auto-detects supported languages in the target directory and dispatches
// to the appropriate language server for each.
type Adapter struct {
	locator  lsploader.Locator
	excludes []string
	logger   *slog.Logger
}

var _ ports.AnalyzerPort = (*Adapter)(nil)

// Option configures the Adapter.
type Option func(*Adapter)

// WithLocator overrides the binary locator (used in tests).
func WithLocator(loc lsploader.Locator) Option {
	return func(a *Adapter) { a.locator = loc }
}

// WithExcludeGlobs sets glob patterns (matched against paths relative to the
// analysis root) that exclude files and directories from the walk.
func WithExcludeGlobs(globs ...string) Option {
	return func(a *Adapter) { a.excludes = append(a.excludes, globs...) }
}

// WithLogger sets the logger used for analysis progress messages.
// Defaults to slog.Default() when not provided.
func WithLogger(l *slog.Logger) Option {
	return func(a *Adapter) { a.logger = l }
}

// New creates an Adapter that resolves language server binaries via exec.LookPath.
func New(opts ...Option) *Adapter {
	a := &Adapter{locator: ExecLocator{}, logger: slog.Default()}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *Adapter) Analyze(ctx context.Context, root string) (domain.Graph, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return domain.Graph{}, fmt.Errorf("resolve root: %w", err)
	}

	langs, err := lsploader.Detect(absRoot)
	if err != nil {
		return domain.Graph{}, fmt.Errorf("detect languages: %w", err)
	}
	if len(langs) == 0 {
		return domain.Graph{}, fmt.Errorf(
			"no supported languages detected in %s (expected go.mod, Cargo.toml, or tsconfig.json)",
			absRoot,
		)
	}

	if err := lsploader.Check(a.locator, langs); err != nil {
		return domain.Graph{}, err
	}

	a.logger.Debug("detected languages", "langs", langs)

	gb := newGraphBuilder()
	for _, lang := range langs {
		if err := a.analyzeWithLSP(ctx, absRoot, lang, gb); err != nil {
			return domain.Graph{}, fmt.Errorf("analyze %s: %w", lang, err)
		}
	}
	// Drop symbols with no cross-file references — noise for this view.
	gb.pruneSymbolsWithoutCrossFileRefs()
	return gb.build(), nil
}

func (a *Adapter) analyzeWithLSP(ctx context.Context, root string, lang lsploader.Language, gb *graphBuilder) error {
	m := lsploader.Meta(lang)
	logger := a.logger.With("lang", string(lang))

	logger.Info("starting language server", "binary", m.LSPBinary)

	cmd := exec.CommandContext(ctx, m.LSPBinary, m.LSPArgs...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", m.LSPBinary, err)
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
	go c.readLoop() //nolint:errcheck

	defer shutdownLSP(cmd, c, tail, stderrDone, logger)

	rootURI := fileURI(root)
	var initResult InitializeResult
	if err := c.call(ctx, "initialize", InitializeParams{
		ProcessID: os.Getpid(),
		RootURI:   rootURI,
		Capabilities: ClientCapability{
			TextDocument: TextDocumentClientCapabilities{
				DocumentSymbol: DocumentSymbolClientCapabilities{
					HierarchicalDocumentSymbolSupport: true,
				},
			},
			Window: WindowClientCapabilities{WorkDoneProgress: true},
		},
	}, &initResult); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("initialized: %w", err)
	}

	// Wait for the server to finish background indexing (e.g. rust-analyzer)
	// before querying symbols and references.
	c.waitForIdle(ctx)

	excludes := make([]string, 0, len(m.DefaultExcludes)+len(a.excludes))
	excludes = append(excludes, m.DefaultExcludes...)
	excludes = append(excludes, a.excludes...)
	files, err := findFiles(root, m.FileExts, excludes)
	if err != nil {
		return fmt.Errorf("find files: %w", err)
	}

	logger.Info("collecting symbols", "files", len(files))
	pass1Start := time.Now()

	// Pass 1: collect all symbols and add "defines" (file→symbol) edges.
	// Keys in fileSymbols are canonicalized so that lookups in pass 2 — which
	// derive their key from gopls' Location.URI — match consistently.
	fileSymbols := make(map[string][]symEntry, len(files))
	var symCount int
	for i, file := range files {
		docURI := fileURI(file)
		fileID := gb.addFileNode(file)

		// Some language servers (e.g. typescript-language-server) require the
		// document to be opened before documentSymbol requests will return results.
		didOpened := false
		if text, err := os.ReadFile(file); err == nil {
			_ = c.notify("textDocument/didOpen", DidOpenTextDocumentParams{
				TextDocument: TextDocumentItem{
					URI:        docURI,
					LanguageID: langIDForFile(lang, file),
					Version:    1,
					Text:       string(text),
				},
			})
			didOpened = true
		}

		var raw symbolResult
		callErr := c.call(ctx, "textDocument/documentSymbol", DocumentSymbolParams{
			TextDocument: TextDocumentIdentifier{URI: docURI},
		}, &raw)

		// Always close if we opened — even on error. Without this, tsserver's
		// open-document working set grows linearly across the loop, making each
		// documentSymbol query O(N) and the whole pass O(N²).
		if didOpened {
			_ = c.notify("textDocument/didClose", DidCloseTextDocumentParams{
				TextDocument: TextDocumentIdentifier{URI: docURI},
			})
		}

		if callErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logger.Debug("documentSymbol failed", "file", file, "err", callErr)
			continue
		}
		syms, err := parseSymbols(raw)
		if err != nil {
			logger.Debug("parse symbols failed", "file", file, "err", err)
			continue
		}
		key := canonPath(file)
		for _, sym := range syms {
			symID := gb.addSymbolNode(sym.Name, file, sym.Range, sym.Kind)
			gb.addEdge(fileID, symID, domain.EdgeKindDefines, domain.ConfidenceExact)
			fileSymbols[key] = append(fileSymbols[key], symEntry{id: symID, sym: sym})
			symCount++
		}
		if (i+1)%progressInterval == 0 {
			logger.Debug("symbols progress", "processed", i+1, "total", len(files))
		}
	}

	logger.Info("symbols collected",
		"files", len(files),
		"count", symCount,
		"elapsed", time.Since(pass1Start),
	)

	// Pass 2: resolve references into symbol→symbol "references" edges.
	if !initResult.Capabilities.ReferencesProvider {
		return nil
	}
	logger.Info("resolving references")
	pass2Start := time.Now()
	edgesBefore := len(gb.edges)
	// typescript-language-server only considers files in its current open set
	// when resolving cross-file references. Pre-open every file before the
	// query loop, then close them all at the end. This is bounded — the open
	// set is the project size, not multiplied per query like Pass 1's
	// documentSymbol issue.
	openedURIs := make([]URI, 0, len(files))
	for _, file := range files {
		text, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		docURI := fileURI(file)
		_ = c.notify("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{
				URI:        docURI,
				LanguageID: langIDForFile(lang, file),
				Version:    1,
				Text:       string(text),
			},
		})
		openedURIs = append(openedURIs, docURI)
	}
	defer func() {
		for _, uri := range openedURIs {
			_ = c.notify("textDocument/didClose", DidCloseTextDocumentParams{
				TextDocument: TextDocumentIdentifier{URI: uri},
			})
		}
	}()

	for i, file := range files {
		docURI := fileURI(file)
		key := canonPath(file)
		for _, entry := range fileSymbols[key] {
			refs, err := getReferences(ctx, c, docURI, entry.sym.SelectionRange.Start)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				logger.Debug("references failed", "file", file, "symbol", entry.sym.Name, "err", err)
				continue
			}
			for _, ref := range refs {
				refFile := canonPath(uriToPath(ref.URI))
				// Cross-file references only — same-file uses are noise.
				if refFile == key {
					continue
				}
				callerID := findContainingSymbol(fileSymbols[refFile], ref.Range.Start)
				if callerID == "" {
					// Reference sits outside any symbol (e.g. module top-level
					// statements); we can't attribute a meaningful caller, skip.
					continue
				}
				gb.addEdge(callerID, entry.id, domain.EdgeKindReferences, domain.ConfidenceProbable)
			}
		}
		if (i+1)%progressInterval == 0 {
			logger.Debug("references progress", "processed", i+1, "total", len(files))
		}
	}
	logger.Info("references resolved",
		"files", len(files),
		"edges", len(gb.edges)-edgesBefore,
		"elapsed", time.Since(pass2Start),
	)
	return nil
}

// symEntry pairs a node ID with the DocumentSymbol it was built from.
type symEntry struct {
	id  string
	sym DocumentSymbol
}

// containsPos reports whether Range r contains Position p.
// LSP ranges are half-open: start is inclusive, end is exclusive.
func containsPos(r Range, p Position) bool {
	if p.Line < r.Start.Line || (p.Line == r.Start.Line && p.Character < r.Start.Character) {
		return false
	}
	if p.Line > r.End.Line || (p.Line == r.End.Line && p.Character >= r.End.Character) {
		return false
	}
	return true
}

// findContainingSymbol returns the ID of the innermost symbol whose Range contains pos,
// or "" if no symbol contains it (top-level reference).
func findContainingSymbol(entries []symEntry, pos Position) string {
	best, bestLines := "", -1
	for _, e := range entries {
		if !containsPos(e.sym.Range, pos) {
			continue
		}
		lines := e.sym.Range.End.Line - e.sym.Range.Start.Line
		if best == "" || lines < bestLines {
			best, bestLines = e.id, lines
		}
	}
	return best
}

// canonPath canonicalizes a filesystem path so map keys produced by walking
// the disk match keys derived from LSP URIs returned by the server.
func canonPath(p string) string {
	if p == "" {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

type edgeKey struct {
	from, to string
	kind     domain.EdgeKind
}

// graphBuilder accumulates nodes and edges, deduplicating file nodes by path
// and (from, to, kind) triples for edges.
type graphBuilder struct {
	nodes   []domain.Node
	edges   []domain.Edge
	fileIDs map[string]string // abs path → node ID
	edgeSet map[edgeKey]bool
}

func newGraphBuilder() *graphBuilder {
	return &graphBuilder{
		fileIDs: make(map[string]string),
		edgeSet: make(map[edgeKey]bool),
	}
}

func (gb *graphBuilder) addFileNode(path string) string {
	if id, ok := gb.fileIDs[path]; ok {
		return id
	}
	id := uuid.NewString()
	gb.nodes = append(gb.nodes, domain.Node{
		ID:    id,
		Kind:  domain.NodeKindFile,
		Label: filepath.Base(path),
		Path:  path,
	})
	gb.fileIDs[path] = id
	return id
}

func (gb *graphBuilder) addSymbolNode(name, file string, r Range, kind SymbolKind) string {
	id := uuid.NewString()
	dr := toDomainRange(r)
	gb.nodes = append(gb.nodes, domain.Node{
		ID:         id,
		Kind:       domain.NodeKindSymbol,
		Label:      name,
		Path:       file,
		SymbolKind: symbolKindName(kind),
		Range:      &dr,
	})
	return id
}

func symbolKindName(k SymbolKind) string {
	switch k {
	case SymbolKindFile:        return "file"
	case SymbolKindModule:      return "module"
	case SymbolKindNamespace:   return "namespace"
	case SymbolKindPackage:     return "package"
	case SymbolKindClass:       return "class"
	case SymbolKindMethod:      return "method"
	case SymbolKindProperty:    return "property"
	case SymbolKindField:       return "field"
	case SymbolKindConstructor: return "constructor"
	case SymbolKindEnum:        return "enum"
	case SymbolKindInterface:   return "interface"
	case SymbolKindFunction:    return "function"
	case SymbolKindVariable:    return "variable"
	case SymbolKindConstant:    return "constant"
	case SymbolKindStruct:      return "struct"
	case SymbolKindTypeParam:   return "typeParameter"
	default:                    return ""
	}
}

func (gb *graphBuilder) addEdge(from, to string, kind domain.EdgeKind, conf domain.Confidence) {
	k := edgeKey{from, to, kind}
	if gb.edgeSet[k] {
		return
	}
	gb.edgeSet[k] = true
	gb.edges = append(gb.edges, domain.Edge{
		ID:         uuid.NewString(),
		From:       from,
		To:         to,
		Kind:       kind,
		Confidence: conf,
	})
}

func (gb *graphBuilder) build() domain.Graph {
	return domain.Graph{Nodes: gb.nodes, Edges: gb.edges}
}

// pruneSymbolsWithoutCrossFileRefs drops symbols isolated to their own file
// — symbols with neither incoming nor outgoing cross-file reference edges.
// Same-file refs are already filtered upstream, so a symbol with no
// `references` edges (in either direction) is pure noise. Symbols with only
// outgoing refs (entry points like main) or only incoming refs (leaf types)
// are kept. File nodes always stay. Only `references` edges are counted —
// `defines` edges would otherwise mark every symbol as connected.
func (gb *graphBuilder) pruneSymbolsWithoutCrossFileRefs() {
	hasRef := make(map[string]bool, len(gb.nodes))
	for _, e := range gb.edges {
		if e.Kind == domain.EdgeKindReferences {
			hasRef[e.From] = true
			hasRef[e.To] = true
		}
	}

	keep := make(map[string]bool, len(gb.nodes))
	for _, n := range gb.nodes {
		if n.Kind != domain.NodeKindSymbol || hasRef[n.ID] {
			keep[n.ID] = true
		}
	}

	newNodes := make([]domain.Node, 0, len(gb.nodes))
	for _, n := range gb.nodes {
		if keep[n.ID] {
			newNodes = append(newNodes, n)
		}
	}
	gb.nodes = newNodes

	newEdges := make([]domain.Edge, 0, len(gb.edges))
	newEdgeSet := make(map[edgeKey]bool, len(gb.edgeSet))
	for _, e := range gb.edges {
		if keep[e.From] && keep[e.To] {
			newEdges = append(newEdges, e)
			newEdgeSet[edgeKey{e.From, e.To, e.Kind}] = true
		}
	}
	gb.edges = newEdges
	gb.edgeSet = newEdgeSet
}

// parseSymbols returns the file's top-level symbols only. Anything
// reachable across files in our supported languages appears as a direct
// child of the file in the DocumentSymbol response; nested entries are
// local scope and noise for the cross-file dependency view.
//
// We rely on hierarchicalDocumentSymbolSupport=true (set at initialize).
// The flat SymbolInformation[] fallback has no hierarchy to filter on,
// so we drop it.
func parseSymbols(raw json.RawMessage) ([]DocumentSymbol, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var docSyms []DocumentSymbol
	if err := json.Unmarshal(raw, &docSyms); err == nil && len(docSyms) > 0 && docSyms[0].SelectionRange != (Range{}) {
		return docSyms, nil
	}

	return nil, nil
}

func getReferences(ctx context.Context, c *conn, docURI URI, pos Position) ([]Location, error) {
	var locs []Location
	err := c.call(ctx, "textDocument/references", ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: docURI},
		Position:     pos,
		Context:      ReferenceContext{IncludeDeclaration: false},
	}, &locs)
	return locs, err
}

// findFiles walks root and collects files whose extension matches any of exts.
// Dot files and dot directories (other than root itself) are always skipped.
// The excludes slice contains doublestar glob patterns matched against paths
// relative to root; matched files and directories are skipped.
func findFiles(root string, exts, excludes []string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Always skip dot entries (except root, when root itself is ".").
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

// langIDForFile returns the LSP languageId for the given file and language.
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

func fileURI(path string) URI {
	u := &url.URL{Scheme: "file", Path: path}
	return u.String()
}

func uriToPath(uri URI) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return u.Path
}

func toDomainRange(r Range) domain.Range {
	return domain.Range{
		Start: domain.Position{Line: r.Start.Line, Character: r.Start.Character},
		End:   domain.Position{Line: r.End.Line, Character: r.End.Character},
	}
}
