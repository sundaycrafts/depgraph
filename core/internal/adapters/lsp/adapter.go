package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/ports"
)

// Adapter implements ports.AnalyzerPort via the Language Server Protocol.
// It manages the LSP server process lifecycle and validates all JSON-RPC responses
// before converting them to domain types.
type Adapter struct {
	serverCmd string
}

var _ ports.AnalyzerPort = (*Adapter)(nil)

// New creates an Adapter using gopls as the language server.
func New() *Adapter {
	return &Adapter{serverCmd: "gopls"}
}

// NewWithServer creates an Adapter with a custom language server command (for testing).
func NewWithServer(cmd string) *Adapter {
	return &Adapter{serverCmd: cmd}
}

func (a *Adapter) Analyze(ctx context.Context, root string) (domain.Graph, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return domain.Graph{}, fmt.Errorf("resolve root: %w", err)
	}

	cmd := exec.CommandContext(ctx, a.serverCmd, "-mode=stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return domain.Graph{}, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return domain.Graph{}, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return domain.Graph{}, fmt.Errorf("start %s: %w", a.serverCmd, err)
	}
	defer cmd.Process.Kill() //nolint:errcheck

	c := newConn(stdout, stdin)
	go c.readLoop() //nolint:errcheck

	// Initialize LSP session.
	rootURI := fileURI(absRoot)
	var initResult InitializeResult
	if err := c.call("initialize", InitializeParams{
		ProcessID: os.Getpid(),
		RootURI:   rootURI,
		Capabilities: ClientCapability{
			TextDocument: TextDocumentClientCapabilities{
				DocumentSymbol: DocumentSymbolClientCapabilities{
					HierarchicalDocumentSymbolSupport: true,
				},
			},
		},
	}, &initResult); err != nil {
		return domain.Graph{}, fmt.Errorf("initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return domain.Graph{}, fmt.Errorf("initialized: %w", err)
	}

	// Walk root for Go source files.
	goFiles, err := findGoFiles(absRoot)
	if err != nil {
		return domain.Graph{}, fmt.Errorf("find go files: %w", err)
	}

	gb := newGraphBuilder()

	for _, file := range goFiles {
		fileURI := fileURI(file)
		fileID := gb.addFileNode(file)

		var raw symbolResult
		if err := c.call("textDocument/documentSymbol", DocumentSymbolParams{
			TextDocument: TextDocumentIdentifier{URI: fileURI},
		}, &raw); err != nil {
			// Non-fatal: skip files the server can't parse.
			continue
		}

		syms, err := parseSymbols(raw)
		if err != nil {
			continue
		}

		for _, sym := range syms {
			symID := gb.addSymbolNode(sym.Name, file, sym.Range)
			gb.addEdge(fileID, symID, domain.EdgeKindDefines, domain.ConfidenceExact)

			if !initResult.Capabilities.ReferencesProvider {
				continue
			}
			refs, err := getReferences(c, fileURI, sym.SelectionRange.Start)
			if err != nil {
				continue
			}
			for _, ref := range refs {
				refFile := uriToPath(ref.URI)
				refFileID := gb.addFileNode(refFile)
				gb.addEdge(refFileID, symID, domain.EdgeKindReferences, domain.ConfidenceProbable)
			}
		}
	}

	return gb.build(), nil
}

// graphBuilder accumulates nodes and edges, deduplicating file nodes by path.
type graphBuilder struct {
	nodes    []domain.Node
	edges    []domain.Edge
	fileIDs  map[string]string // abs path → node ID
}

func newGraphBuilder() *graphBuilder {
	return &graphBuilder{fileIDs: make(map[string]string)}
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

func (gb *graphBuilder) addSymbolNode(name, file string, r Range) string {
	id := uuid.NewString()
	dr := toDomainRange(r)
	gb.nodes = append(gb.nodes, domain.Node{
		ID:    id,
		Kind:  domain.NodeKindSymbol,
		Label: name,
		Path:  file,
		Range: &dr,
	})
	return id
}

func (gb *graphBuilder) addEdge(from, to string, kind domain.EdgeKind, conf domain.Confidence) {
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

// parseSymbols handles both []DocumentSymbol and []SymbolInformation.
// It flattens hierarchical symbols into a flat list.
func parseSymbols(raw json.RawMessage) ([]DocumentSymbol, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var docSyms []DocumentSymbol
	if err := json.Unmarshal(raw, &docSyms); err == nil && len(docSyms) > 0 && docSyms[0].SelectionRange != (Range{}) {
		return flattenSymbols(docSyms), nil
	}

	var symInfos []SymbolInformation
	if err := json.Unmarshal(raw, &symInfos); err != nil {
		return nil, fmt.Errorf("parse symbols: %w", err)
	}
	result := make([]DocumentSymbol, len(symInfos))
	for i, si := range symInfos {
		result[i] = DocumentSymbol{
			Name:           si.Name,
			Kind:           si.Kind,
			Range:          si.Location.Range,
			SelectionRange: si.Location.Range,
		}
	}
	return result, nil
}

func flattenSymbols(syms []DocumentSymbol) []DocumentSymbol {
	var result []DocumentSymbol
	for _, s := range syms {
		result = append(result, s)
		if len(s.Children) > 0 {
			result = append(result, flattenSymbols(s.Children)...)
		}
	}
	return result
}

func getReferences(c *conn, docURI URI, pos Position) ([]Location, error) {
	var locs []Location
	err := c.call("textDocument/references", ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: docURI},
		Position:     pos,
		Context:      ReferenceContext{IncludeDeclaration: false},
	}, &locs)
	return locs, err
}

func findGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
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
