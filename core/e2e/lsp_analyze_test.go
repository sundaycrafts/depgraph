package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	lspadapter "github.com/sundaycrafts/depgraph/internal/adapters/lsp"
	"github.com/sundaycrafts/depgraph/internal/domain"
)

// TestMain installs TypeScript into the ts-project fixture before any test runs.
func TestMain(m *testing.M) {
	if tsFixture, err := filepath.Abs("testdata/ts-project"); err == nil {
		cmd := exec.Command("npm", "install")
		cmd.Dir = tsFixture
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "npm install: %v\n%s\n", err, out)
		}
	}
	os.Exit(m.Run())
}

// TestAnalyze_TypeScript verifies that the LSP adapter correctly identifies
// symbols and references in a two-file TypeScript project:
//   - greeter.ts exports greet()
//   - index.ts imports and calls greet() inside main()
//
// Expected: graph contains a "greet" symbol and a "references" edge pointing to it.
func TestAnalyze_TypeScript(t *testing.T) {
	root, err := filepath.Abs("testdata/ts-project")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	graph, err := lspadapter.New(lspadapter.WithExcludeGlobs("node_modules/**")).Analyze(ctx, root)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	greet := findSymbol(graph, "greet")
	if greet == nil {
		t.Fatalf("symbol 'greet' not found in graph; symbols present: %v", symbolLabels(graph))
	}
	if !hasReferenceTo(graph, greet.ID) {
		t.Errorf("expected a 'references' edge pointing to 'greet', found none")
	}
}

// TestAnalyze_Rust verifies that the LSP adapter correctly identifies
// symbols and references in a single-file Rust library:
//   - lib.rs defines add() and double(), where double calls add
//
// Expected: graph contains an "add" symbol and a "references" edge pointing to it.
func TestAnalyze_Rust(t *testing.T) {
	root, err := filepath.Abs("testdata/rust-project")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	graph, err := lspadapter.New().Analyze(ctx, root)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	add := findSymbol(graph, "add")
	if add == nil {
		t.Fatalf("symbol 'add' not found in graph; symbols present: %v", symbolLabels(graph))
	}
	if !hasReferenceTo(graph, add.ID) {
		t.Errorf("expected a 'references' edge pointing to 'add', found none")
	}
}

func findSymbol(g domain.Graph, label string) *domain.Node {
	for i, n := range g.Nodes {
		if n.Kind == domain.NodeKindSymbol && n.Label == label {
			return &g.Nodes[i]
		}
	}
	return nil
}

func hasReferenceTo(g domain.Graph, nodeID string) bool {
	for _, e := range g.Edges {
		if e.To == nodeID && e.Kind == domain.EdgeKindReferences {
			return true
		}
	}
	return false
}

func symbolLabels(g domain.Graph) []string {
	var labels []string
	for _, n := range g.Nodes {
		if n.Kind == domain.NodeKindSymbol {
			labels = append(labels, fmt.Sprintf("%s(%s)", n.Label, n.SymbolKind))
		}
	}
	return labels
}
