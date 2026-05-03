// Package cache provides content-addressable caching of analysis results.
// A Wrapper around any AnalyzerPort short-circuits Analyze when the source
// tree fingerprint matches a previously stored graph, eliminating the
// LSP cold-start cost on repeated runs against unchanged code.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
	"github.com/sundaycrafts/depgraph/internal/ports"
)

// Wrapper decorates an AnalyzerPort with on-disk caching keyed by a
// fingerprint of the source tree.
type Wrapper struct {
	inner    ports.AnalyzerPort
	dir      string
	version  string
	excludes []string
	enabled  bool
	logger   *slog.Logger
}

var _ ports.AnalyzerPort = (*Wrapper)(nil)

// Option configures the Wrapper.
type Option func(*Wrapper)

// WithVersion sets the depgraph version used as part of the fingerprint
// so cached graphs are invalidated across upgrades.
func WithVersion(v string) Option { return func(w *Wrapper) { w.version = v } }

// WithDir overrides the cache directory (default: $XDG_CACHE_HOME/depgraph).
func WithDir(d string) Option { return func(w *Wrapper) { w.dir = d } }

// WithExcludes sets the user-supplied exclude globs that affect both the
// inner analyzer's file walk and the fingerprint computation here.
func WithExcludes(e []string) Option { return func(w *Wrapper) { w.excludes = e } }

// WithDisabled bypasses the cache entirely; every call goes to the inner
// analyzer.
func WithDisabled() Option { return func(w *Wrapper) { w.enabled = false } }

// WithLogger sets the logger used for cache hit/miss messages.
func WithLogger(l *slog.Logger) Option { return func(w *Wrapper) { w.logger = l } }

func defaultDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "depgraph")
	}
	return filepath.Join(os.TempDir(), "depgraph-cache")
}

// New wraps inner with caching enabled by default.
func New(inner ports.AnalyzerPort, opts ...Option) *Wrapper {
	w := &Wrapper{
		inner:   inner,
		dir:     defaultDir(),
		enabled: true,
		logger:  slog.Default(),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Analyze checks the cache for a graph matching the source-tree fingerprint
// and returns it on hit; on miss it delegates to the inner analyzer and
// writes the result to disk before returning.
func (w *Wrapper) Analyze(ctx context.Context, root string) (domain.Graph, error) {
	if !w.enabled {
		return w.inner.Analyze(ctx, root)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return w.inner.Analyze(ctx, root)
	}

	fp, err := w.computeFingerprint(absRoot)
	if err != nil {
		w.logger.Warn("could not compute cache fingerprint, bypassing cache", "err", err)
		return w.inner.Analyze(ctx, root)
	}

	cachePath := filepath.Join(w.dir, fp+".json")
	if graph, err := loadGraph(cachePath); err == nil {
		w.logger.Info("cache hit", "fingerprint", fp)
		return graph, nil
	}
	w.logger.Debug("cache miss", "fingerprint", fp)

	graph, err := w.inner.Analyze(ctx, root)
	if err != nil {
		return graph, err
	}

	if err := saveGraph(cachePath, graph); err != nil {
		w.logger.Warn("could not write cache", "err", err)
	} else {
		w.logger.Debug("cache stored", "fingerprint", fp, "path", cachePath)
	}
	return graph, nil
}

// computeFingerprint walks root and hashes (relpath, size, mtime) of every
// source file in supported languages, plus marker files (tsconfig.json etc.),
// the depgraph version, and the sorted exclude list. Hidden entries and
// directories matching either user excludes or per-language DefaultExcludes
// are skipped — the same skip rules the inner analyzer applies.
func (w *Wrapper) computeFingerprint(root string) (string, error) {
	exts := make(map[string]bool)
	markers := make(map[string]bool)
	var defaultExcludes []string
	for _, lang := range lsploader.All() {
		m := lsploader.Meta(lang)
		for _, ext := range m.FileExts {
			exts[ext] = true
		}
		for _, marker := range m.MarkerFiles {
			markers[marker] = true
		}
		defaultExcludes = append(defaultExcludes, m.DefaultExcludes...)
	}
	allExcludes := append([]string{}, defaultExcludes...)
	allExcludes = append(allExcludes, w.excludes...)

	type fileEntry struct {
		rel  string
		size int64
		mod  int64
	}
	var entries []fileEntry

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		for _, p := range allExcludes {
			if ok, _ := doublestar.PathMatch(p, rel); ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}

		ext := filepath.Ext(d.Name())
		if !exts[ext] && !markers[d.Name()] {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, fileEntry{rel: rel, size: info.Size(), mod: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	excludesSorted := append([]string{}, w.excludes...)
	sort.Strings(excludesSorted)

	h := sha256.New()
	fmt.Fprintf(h, "version=%s\n", w.version)
	for _, e := range excludesSorted {
		fmt.Fprintf(h, "exclude=%s\n", e)
	}
	for _, e := range entries {
		fmt.Fprintf(h, "%s|%d|%d\n", e.rel, e.size, e.mod)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func loadGraph(path string) (domain.Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Graph{}, err
	}
	var graph domain.Graph
	if err := json.Unmarshal(data, &graph); err != nil {
		return domain.Graph{}, err
	}
	return graph, nil
}

func saveGraph(path string, graph domain.Graph) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(graph)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
