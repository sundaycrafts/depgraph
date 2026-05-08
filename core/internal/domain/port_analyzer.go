package domain

import "context"

// PortAnalyzer abstracts the static-analysis backend (LSP, Tree-sitter,
// etc.) used by the HTTP one-shot path. Implementations are responsible
// for starting and stopping any required processes within Analyze.
type PortAnalyzer interface {
	Analyze(ctx context.Context, root string) (Graph, error)
}

// PortAnalyzerFunc is a function adapter for PortAnalyzer.
type PortAnalyzerFunc func(ctx context.Context, root string) (Graph, error)

// Analyze satisfies PortAnalyzer.
func (f PortAnalyzerFunc) Analyze(ctx context.Context, root string) (Graph, error) {
	return f(ctx, root)
}
