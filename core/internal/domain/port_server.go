package domain

import "context"

// PortServer abstracts a long-running server process (HTTP, MCP stdio,
// etc.). main.go selects an implementation by mode and calls Serve once.
type PortServer interface {
	Serve(ctx context.Context) error
}
