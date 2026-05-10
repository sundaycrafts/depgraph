package domain

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// Workspace is the aggregate of every Project registered in one depgraph
// session (a long-lived MCP connection, an HTTP request, etc.). It owns
// the factories that build per-project AnalysisSessions and SourceWatchers
// — domain code never instantiates LSP processes or fsnotify watchers
// directly.
type Workspace struct {
	sessions PortAnalysisSessionFactory
	watchers PortSourceWatcherFactory
	detector PortLanguageDetector
	locator  lsploader.Locator
	logger   *slog.Logger

	parent  context.Context
	rootCtx context.CancelFunc

	mu       sync.RWMutex
	projects map[string]*Project
}

// NewWorkspace builds a Workspace bound to parent. The workspace's
// factories must be supplied at construction; if any are nil the
// workspace will reject AddProject calls. Cancelling parent cascades to
// every project: in-flight session launches abort and Shutdown is
// invoked transitively.
func NewWorkspace(
	parent context.Context,
	sessions PortAnalysisSessionFactory,
	watchers PortSourceWatcherFactory,
	detector PortLanguageDetector,
	locator lsploader.Locator,
	logger *slog.Logger,
) *Workspace {
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Workspace{
		sessions: sessions,
		watchers: watchers,
		detector: detector,
		locator:  locator,
		logger:   logger,
		parent:   ctx,
		rootCtx:  cancel,
		projects: make(map[string]*Project),
	}
}

// Get returns the Project registered for root, or nil if absent. The
// argument may be a relative path; it is resolved before lookup.
func (w *Workspace) Get(root string) *Project {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.projects[abs]
}

// List returns every registered project in deterministic order (by Root).
func (w *Workspace) List() []*Project {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]*Project, 0, len(w.projects))
	for _, p := range w.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

// AddProject registers root as a Project and starts an AnalysisSession
// per detected language asynchronously. The returned Project is
// observable immediately; per-language sessions transition Indexing →
// Ready/Failed in the background.
//
// excludeSymbols are `[kind:]pattern` specs (see SymbolExclude) used to
// skip individual symbols during BFS — e.g. Next.js convention methods
// that would otherwise fan out across every page.
//
// Re-adding an already-registered root is a no-op (returns the existing
// Project). Filesystem-watcher creation failures cause AddProject to
// fail outright — running with a stale view would silently return out-
// of-date results to callers.
func (w *Workspace) AddProject(root string, excludes, excludeSymbols []string) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	w.mu.Lock()
	if existing, ok := w.projects[abs]; ok {
		w.mu.Unlock()
		return existing, nil
	}

	langs, err := w.detector.Detect(abs)
	if err != nil {
		w.mu.Unlock()
		return nil, fmt.Errorf("detect languages: %w", err)
	}
	if len(langs) == 0 {
		w.mu.Unlock()
		return nil, fmt.Errorf("no supported languages detected in %s (expected go.mod, Cargo.toml, or tsconfig.json)", abs)
	}
	if err := lsploader.Check(w.locator, langs); err != nil {
		w.mu.Unlock()
		return nil, err
	}

	// Watcher excludes: combine user excludes with the union of every
	// detected language's defaults so we skip the same trees the analysis
	// path skips (vendor/, target/, node_modules/ etc.).
	allExcludes := append([]string(nil), excludes...)
	for _, lang := range langs {
		allExcludes = append(allExcludes, lsploader.Meta(lang).DefaultExcludes...)
	}
	watcher, err := w.watchers.Watch(abs, allExcludes)
	if err != nil {
		w.mu.Unlock()
		return nil, fmt.Errorf("start file watcher: %w", err)
	}

	project := newProject(abs, excludes, excludeSymbols, langs, watcher, w.logger.With("root", abs))
	w.projects[abs] = project
	w.mu.Unlock()

	for _, lang := range langs {
		go w.launchSession(project, lang)
	}
	return project, nil
}

// launchSession opens an AnalysisSession for lang via the configured
// factory and installs it into project on success. Failures are recorded
// on the project's session entry.
func (w *Workspace) launchSession(project *Project, lang lsploader.Language) {
	logger := w.logger.With("lang", string(lang), "root", project.Root)
	logger.Info("starting analysis session")

	events := project.SubscribeFiles()
	sess, err := w.sessions.Open(w.parent, lang, project.Root, project.Excludes, events)
	if err != nil {
		project.markFailed(lang, fmt.Errorf("open session: %w", err))
		return
	}
	if w.parent.Err() != nil {
		sess.Shutdown()
		return
	}
	project.markReady(lang, sess)
	logger.Info("analysis session ready")
}

// Shutdown stops every project's watcher and tears down every session.
// Cancels the workspace context first so any in-flight launchSession
// goroutines short-circuit before issuing more LSP requests.
func (w *Workspace) Shutdown() {
	w.rootCtx()

	w.mu.Lock()
	projects := make([]*Project, 0, len(w.projects))
	for _, p := range w.projects {
		projects = append(projects, p)
	}
	w.projects = make(map[string]*Project)
	w.mu.Unlock()

	var wg sync.WaitGroup
	for _, p := range projects {
		wg.Add(1)
		go func(pr *Project) {
			defer wg.Done()
			pr.Shutdown()
		}(p)
	}
	wg.Wait()
}
