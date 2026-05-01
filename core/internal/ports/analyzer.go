package ports

import (
	"context"

	"github.com/sundaycrafts/depgraph/internal/domain"
)

// AnalyzerPort abstracts the static analysis backend (LSP, Tree-sitter, etc.).
// Implementations are responsible for starting/stopping any required processes.
type AnalyzerPort interface {
	Analyze(ctx context.Context, root string) (domain.Graph, error)
}
