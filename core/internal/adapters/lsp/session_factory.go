package lsp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// SessionFactory is the LSP-backed implementation of
// domain.PortAnalysisSessionFactory. Construct via NewSessionFactory.
type SessionFactory struct {
	cfg    *Config
	logger *slog.Logger
}

var _ domain.PortAnalysisSessionFactory = (*SessionFactory)(nil)

// NewSessionFactory builds a factory rooted in cfg. cfg supplies the
// per-language LSP command, args, and initialization options.
func NewSessionFactory(cfg *Config, logger *slog.Logger) *SessionFactory {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionFactory{cfg: cfg, logger: logger}
}

// Open launches an LSP session for lang, performs the initialize
// handshake, waits for indexing to settle, and returns a session ready
// to answer DocumentSymbol / References queries.
//
// For languages whose meta sets PreloadFiles (e.g. TypeScript, Python)
// the construction also pre-opens every project file (their servers
// require this for cross-file references) and starts a goroutine
// consuming `events` to keep the LSP's view in sync with disk.
func (f *SessionFactory) Open(
	ctx context.Context,
	lang lsploader.Language,
	root string,
	excludes []string,
	events <-chan domain.SourceChange,
) (domain.PortAnalysisSession, error) {
	specs, err := f.cfg.LSPSpecsFor(lang)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, errors.New("no LSP servers configured")
	}
	// Multiple language_servers per language is supported in the zed
	// config schema; for now we use the first one and ignore the rest.
	spec := specs[0]
	sess, err := startLiveSession(ctx, lang, root, excludes, spec, events, f.logger)
	if err != nil {
		return nil, fmt.Errorf("start LSP: %w", err)
	}
	return sess, nil
}
