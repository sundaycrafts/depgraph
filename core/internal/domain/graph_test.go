package domain_test

import (
	"testing"

	"github.com/sundaycrafts/depgraph/internal/domain"
)

func TestNodeKindValues(t *testing.T) {
	if domain.NodeKindFile != "file" {
		t.Errorf("NodeKindFile = %q, want %q", domain.NodeKindFile, "file")
	}
	if domain.NodeKindSymbol != "symbol" {
		t.Errorf("NodeKindSymbol = %q, want %q", domain.NodeKindSymbol, "symbol")
	}
}

func TestEdgeKindValues(t *testing.T) {
	if domain.EdgeKindDefines != "defines" {
		t.Errorf("EdgeKindDefines = %q, want %q", domain.EdgeKindDefines, "defines")
	}
	if domain.EdgeKindReferences != "references" {
		t.Errorf("EdgeKindReferences = %q, want %q", domain.EdgeKindReferences, "references")
	}
}

func TestConfidenceValues(t *testing.T) {
	if domain.ConfidenceExact != "exact" {
		t.Errorf("ConfidenceExact = %q, want %q", domain.ConfidenceExact, "exact")
	}
	if domain.ConfidenceProbable != "probable" {
		t.Errorf("ConfidenceProbable = %q, want %q", domain.ConfidenceProbable, "probable")
	}
}

func TestGraphZeroValue(t *testing.T) {
	var g domain.Graph
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Error("zero-value Graph should have no nodes or edges")
	}
}

func TestNodeWithRange(t *testing.T) {
	n := domain.Node{
		ID:    "n1",
		Kind:  domain.NodeKindSymbol,
		Label: "MyFunc",
		Path:  "/src/main.go",
		Range: &domain.Range{
			Start: domain.Position{Line: 0, Character: 0},
			End:   domain.Position{Line: 5, Character: 1},
		},
	}
	if n.Range == nil {
		t.Fatal("expected non-nil Range")
	}
	if n.Range.Start.Line != 0 || n.Range.End.Line != 5 {
		t.Errorf("unexpected range lines: start=%d end=%d", n.Range.Start.Line, n.Range.End.Line)
	}
}
