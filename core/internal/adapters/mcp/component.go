package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// ComponentState is the high-level lifecycle of a component (the union over
// the component's per-language sessions).
type ComponentState string

const (
	ComponentIndexing ComponentState = "indexing"
	ComponentReady    ComponentState = "ready"
	ComponentFailed   ComponentState = "failed"
)

// Component is one LSP root tracked by the MCP server.
type Component struct {
	Root     string
	Excludes []string

	mu       sync.RWMutex
	sessions map[lsploader.Language]*sessionEntry
}

// sessionEntry tracks a single (language) session within a component.
type sessionEntry struct {
	mu      sync.RWMutex
	state   ComponentState
	err     error
	session *LiveSession // nil while indexing or after failure
}

func (c *Component) snapshotSessions() map[lsploader.Language]*sessionEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[lsploader.Language]*sessionEntry, len(c.sessions))
	for k, v := range c.sessions {
		out[k] = v
	}
	return out
}

// State reports the aggregated state of the component.
func (c *Component) State() (ComponentState, error) {
	entries := c.snapshotSessions()
	if len(entries) == 0 {
		return ComponentIndexing, nil
	}
	ready := 0
	var firstErr error
	for _, e := range entries {
		e.mu.RLock()
		st := e.state
		err := e.err
		e.mu.RUnlock()
		switch st {
		case ComponentFailed:
			if firstErr == nil {
				firstErr = err
			}
		case ComponentReady:
			ready++
		}
	}
	switch {
	case firstErr != nil:
		return ComponentFailed, firstErr
	case ready == len(entries):
		return ComponentReady, nil
	default:
		return ComponentIndexing, nil
	}
}

// readySession returns the LiveSession for lang if it is in Ready state.
func (c *Component) readySession(lang lsploader.Language) (*LiveSession, error) {
	c.mu.RLock()
	entry := c.sessions[lang]
	c.mu.RUnlock()
	if entry == nil {
		return nil, fmt.Errorf("no session for language %s", lang)
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	switch entry.state {
	case ComponentReady:
		return entry.session, nil
	case ComponentFailed:
		return nil, fmt.Errorf("session for %s failed: %w", lang, entry.err)
	default:
		return nil, fmt.Errorf("session for %s is still indexing", lang)
	}
}

// readySessions returns every language whose session is in Ready state.
func (c *Component) readySessions() []*LiveSession {
	entries := c.snapshotSessions()
	out := make([]*LiveSession, 0, len(entries))
	for _, e := range entries {
		e.mu.RLock()
		if e.state == ComponentReady && e.session != nil {
			out = append(out, e.session)
		}
		e.mu.RUnlock()
	}
	return out
}

// ComponentManager tracks every component registered in the MCP session and
// owns the corresponding LiveSession lifetimes.
type ComponentManager struct {
	cfg      *Config
	locator  lsploader.Locator
	logger   *slog.Logger
	parent   context.Context // cancelled at session shutdown
	rootCtx  context.CancelFunc

	mu         sync.RWMutex
	components map[string]*Component
}

// NewComponentManager builds a ComponentManager whose component lifetimes
// are bound to parent. When parent is cancelled, ShutdownAll runs; callers
// usually invoke ShutdownAll directly from Adapter.Serve before returning.
func NewComponentManager(parent context.Context, cfg *Config, locator lsploader.Locator, logger *slog.Logger) *ComponentManager {
	ctx, cancel := context.WithCancel(parent)
	return &ComponentManager{
		cfg:        cfg,
		locator:    locator,
		logger:     logger,
		parent:     ctx,
		rootCtx:    cancel,
		components: make(map[string]*Component),
	}
}

// Get returns the component registered for root, or nil if absent.
func (m *ComponentManager) Get(root string) *Component {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.components[root]
}

// List returns components in deterministic order (by root path).
func (m *ComponentManager) List() []*Component {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Component, 0, len(m.components))
	for _, c := range m.components {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

// AddComponent registers root as a component and starts an LSP per detected
// language asynchronously. The returned Component is observable immediately;
// individual sessions transition Indexing→Ready/Failed in the background.
//
// If a component is already registered for root, AddComponent returns the
// existing entry without re-launching its sessions. Re-adding is a no-op.
func (m *ComponentManager) AddComponent(root string, excludes []string) (*Component, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	m.mu.Lock()
	if existing, ok := m.components[abs]; ok {
		m.mu.Unlock()
		return existing, nil
	}

	langs, err := lsploader.Detect(abs)
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("detect languages: %w", err)
	}
	if len(langs) == 0 {
		m.mu.Unlock()
		return nil, fmt.Errorf("no supported languages detected in %s (expected go.mod, Cargo.toml, or tsconfig.json)", abs)
	}
	if err := lsploader.Check(m.locator, langs); err != nil {
		m.mu.Unlock()
		return nil, err
	}

	comp := &Component{
		Root:     abs,
		Excludes: append([]string(nil), excludes...),
		sessions: make(map[lsploader.Language]*sessionEntry),
	}
	for _, lang := range langs {
		comp.sessions[lang] = &sessionEntry{state: ComponentIndexing}
	}
	m.components[abs] = comp
	m.mu.Unlock()

	for _, lang := range langs {
		go m.launchSession(comp, lang)
	}
	return comp, nil
}

func (m *ComponentManager) launchSession(comp *Component, lang lsploader.Language) {
	logger := m.logger.With("lang", string(lang), "root", comp.Root)

	specs, err := m.cfg.LSPSpecsFor(lang)
	if err != nil {
		m.markFailed(comp, lang, err)
		return
	}
	if len(specs) == 0 {
		m.markFailed(comp, lang, errors.New("no LSP servers configured"))
		return
	}
	// Multiple language_servers per language is possible in the zed-style
	// config; for now we use the first one and ignore the rest. Future work
	// can fan out to all of them.
	spec := specs[0]

	logger.Info("starting LSP session", "binary", spec.Command)
	sess, err := startLiveSession(m.parent, lang, comp.Root, comp.Excludes, spec, logger)
	if err != nil {
		m.markFailed(comp, lang, fmt.Errorf("start LSP: %w", err))
		return
	}
	if m.parent.Err() != nil {
		sess.Shutdown()
		return
	}

	m.markReady(comp, lang, sess)
	logger.Info("LSP session ready")
}

func (m *ComponentManager) markReady(comp *Component, lang lsploader.Language, sess *LiveSession) {
	comp.mu.RLock()
	entry := comp.sessions[lang]
	comp.mu.RUnlock()
	if entry == nil {
		sess.Shutdown()
		return
	}
	entry.mu.Lock()
	entry.state = ComponentReady
	entry.session = sess
	entry.err = nil
	entry.mu.Unlock()
}

func (m *ComponentManager) markFailed(comp *Component, lang lsploader.Language, err error) {
	m.logger.Error("LSP session failed", "root", comp.Root, "lang", string(lang), "err", err)
	comp.mu.RLock()
	entry := comp.sessions[lang]
	comp.mu.RUnlock()
	if entry == nil {
		return
	}
	entry.mu.Lock()
	entry.state = ComponentFailed
	entry.err = err
	entry.session = nil
	entry.mu.Unlock()
}

// ShutdownAll terminates every registered LSP session. Safe to call multiple
// times. Cancels the parent context first so any pending launchSession
// goroutines short-circuit before issuing more LSP requests.
func (m *ComponentManager) ShutdownAll() {
	m.rootCtx()

	m.mu.Lock()
	comps := make([]*Component, 0, len(m.components))
	for _, c := range m.components {
		comps = append(comps, c)
	}
	m.components = make(map[string]*Component)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, comp := range comps {
		for _, entry := range comp.snapshotSessions() {
			entry.mu.Lock()
			sess := entry.session
			entry.session = nil
			entry.state = ComponentFailed
			entry.err = errors.New("shutdown")
			entry.mu.Unlock()
			if sess == nil {
				continue
			}
			wg.Add(1)
			go func(s *LiveSession) {
				defer wg.Done()
				s.Shutdown()
			}(sess)
		}
	}
	wg.Wait()
}
