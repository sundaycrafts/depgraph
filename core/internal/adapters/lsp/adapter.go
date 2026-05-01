package lsp

import (
	"context"

	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/ports"
)

// Adapter implements ports.AnalyzerPort via the Language Server Protocol.
// It manages the LSP server process lifecycle and validates all JSON-RPC responses
// before converting them to domain types.
type Adapter struct{}

var _ ports.AnalyzerPort = (*Adapter)(nil)

func New() *Adapter {
	return &Adapter{}
}

func (a *Adapter) Analyze(_ context.Context, _ string) (domain.Graph, error) {
	panic("not implemented")
}
