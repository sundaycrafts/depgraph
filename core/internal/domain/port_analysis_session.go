package domain

import (
	"context"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// ReferenceLocation is one site at which a symbol is referenced. Returned
// by PortAnalysisSession.References for use by the find_references BFS.
// (Value type, not a port — kept here next to the port that produces it.)
type ReferenceLocation struct {
	URI   string
	Range Range
}

// PortAnalysisSession is a long-lived analysis backend dedicated to a
// single (project root, language) pair. The current production
// implementation is LSP-based (`adapters/lsp`), but the abstraction is
// deliberately not LSP-named so future backends (tree-sitter, IDE
// protocols) can implement it.
//
// Implementations must be safe for concurrent use by multiple goroutines.
type PortAnalysisSession interface {
	// Lang reports the language this session was started for.
	Lang() lsploader.Language

	// DocumentSymbol returns the hierarchical symbol tree for uri. uri is
	// a file:// URI; whether the implementation reads that path from disk
	// or relies on a previously-opened buffer is implementation-defined.
	DocumentSymbol(ctx context.Context, uri string) ([]DocumentSymbol, error)

	// References returns every site at which the symbol at (uri, pos) is
	// referenced. Implementations may need to maintain an open-file set
	// (see the LSP impl's runEventLoop) for cross-file references to
	// surface; that is encapsulated within the session.
	References(ctx context.Context, uri string, pos Position) ([]ReferenceLocation, error)

	// Shutdown closes the session, releasing any subprocess or buffers.
	// Safe to call multiple times.
	Shutdown()
}

// PortAnalysisSessionFactory builds a PortAnalysisSession for a given
// language. The returned session is already initialised, has finished
// its initial indexing pass, and is consuming `events` to keep its
// in-memory state in sync with disk.
type PortAnalysisSessionFactory interface {
	Open(
		ctx context.Context,
		lang lsploader.Language,
		root string,
		excludes []string,
		events <-chan SourceChange,
	) (PortAnalysisSession, error)
}
