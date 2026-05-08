package mcp

import (
	"context"

	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// ProjectInitializer is the future hook for kicking off LSP startup the
// moment an MCP session is established. The eventual implementation is
// expected to walk root (respecting .gitignore), discover nested
// projects (subdirectories that contain a marker file for one of langs),
// and register each one with the supplied Workspace.
//
// For now the only implementation is a no-op so the wiring in server.go
// can be exercised end-to-end without committing to a particular
// auto-discovery strategy.
type ProjectInitializer interface {
	// Initialize is invoked from the MCP `initialize` request handler,
	// with the directory the depgraph process was launched from and the
	// set of languages this build supports. Implementations should be
	// non-blocking; long-running work belongs in goroutines.
	Initialize(ctx context.Context, root string, langs []lsploader.Language, workspace *domain.Workspace) error

	// Shutdown is invoked when the MCP session ends. Implementations
	// should release any resources they own. Workspace.Shutdown handles
	// project / session lifetimes; this hook is for initializer-owned
	// resources only (file watchers, gitignore parsers, etc.).
	Shutdown(ctx context.Context) error
}

// NewNoopInitializer returns a ProjectInitializer that does nothing. It
// is the default until the recursive project-discovery feature lands.
func NewNoopInitializer() ProjectInitializer { return noopInitializer{} }

type noopInitializer struct{}

func (noopInitializer) Initialize(_ context.Context, _ string, _ []lsploader.Language, _ *domain.Workspace) error {
	return nil
}

func (noopInitializer) Shutdown(_ context.Context) error { return nil }
