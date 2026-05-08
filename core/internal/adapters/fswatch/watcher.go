// Package fswatch implements domain.PortSourceWatcher on top of fsnotify.
package fswatch

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
	"github.com/sundaycrafts/depgraph/internal/domain"
)

// Factory implements domain.PortSourceWatcherFactory using fsnotify.
type Factory struct {
	logger *slog.Logger
}

var _ domain.PortSourceWatcherFactory = (*Factory)(nil)

// NewFactory builds a Factory whose constructed watchers log via logger.
// A nil logger falls back to slog.Default.
func NewFactory(logger *slog.Logger) *Factory {
	if logger == nil {
		logger = slog.Default()
	}
	return &Factory{logger: logger}
}

// Watch satisfies domain.PortSourceWatcherFactory.
func (f *Factory) Watch(root string, excludes []string) (domain.PortSourceWatcher, error) {
	return newWatcher(root, excludes, f.logger)
}

// watcher monitors a project root via fsnotify and fans out events to
// multiple subscribers as domain.SourceChange. Directory watches are
// added recursively at construction; subdirectories created at runtime
// are picked up via the CREATE event handler.
//
// Drop-on-full semantics: if a subscriber's buffer is full we drop the
// event and warn. Subsequent Modify events are idempotent (LSP-side
// runEventLoop resends full content) so a single dropped Modify recovers
// on the next save; a dropped Create / Delete is rare enough to accept.
type watcher struct {
	root     string
	excludes []string
	logger   *slog.Logger

	fsw *fsnotify.Watcher

	mu      sync.Mutex
	subs    []chan domain.SourceChange
	watched map[string]bool
	closed  bool
	done    chan struct{}
}

var _ domain.PortSourceWatcher = (*watcher)(nil)

// newWatcher walks root respecting excludes (using the same dot-skip and
// doublestar rules as domain.WalkSourceFiles), adds an fsnotify watch
// for every visited directory, and starts a fan-out goroutine.
func newWatcher(root string, excludes []string, logger *slog.Logger) (*watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	w := &watcher{
		root:     root,
		excludes: append([]string(nil), excludes...),
		logger:   logger.With("watcher", root),
		fsw:      fsw,
		watched:  make(map[string]bool),
		done:     make(chan struct{}),
	}
	dirs, err := domain.WalkDirectories(root, w.excludes)
	if err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	for _, d := range dirs {
		if err := w.addDir(d); err != nil {
			_ = fsw.Close()
			return nil, fmt.Errorf("watch %s: %w", d, err)
		}
	}
	go w.run()
	return w, nil
}

// Subscribe satisfies domain.PortSourceWatcher.
func (w *watcher) Subscribe() <-chan domain.SourceChange {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := make(chan domain.SourceChange, 64)
	if w.closed {
		close(ch)
		return ch
	}
	w.subs = append(w.subs, ch)
	return ch
}

// Stop satisfies domain.PortSourceWatcher.
func (w *watcher) Stop() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	subs := w.subs
	w.subs = nil
	w.mu.Unlock()

	_ = w.fsw.Close()
	<-w.done

	for _, ch := range subs {
		close(ch)
	}
}

func (w *watcher) addDir(dir string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.watched[dir] {
		return nil
	}
	if err := w.fsw.Add(dir); err != nil {
		return err
	}
	w.watched[dir] = true
	return nil
}

func (w *watcher) run() {
	defer close(w.done)
	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.logger.Warn("fsnotify error", "err", err)
		}
	}
}

// handle classifies a raw fsnotify event into our FileOp vocabulary and
// dispatches it (or, for new directories, walks the subtree to add fresh
// watches and emit creates for any files already inside).
func (w *watcher) handle(ev fsnotify.Event) {
	if !w.allowed(ev.Name) {
		return
	}

	create := ev.Op&fsnotify.Create != 0
	write := ev.Op&fsnotify.Write != 0
	remove := ev.Op&fsnotify.Remove != 0
	rename := ev.Op&fsnotify.Rename != 0

	switch {
	case create:
		// Newly-created entry. If it's a directory, recurse to pick up
		// any contents that landed before our watch was added.
		info, err := os.Stat(ev.Name)
		if err == nil && info.IsDir() {
			w.handleNewDir(ev.Name)
			return
		}
		w.publish(domain.SourceChange{Path: ev.Name, Op: domain.FileCreated})
	case remove, rename:
		// fsnotify emits Rename for the source path of a rename; the
		// destination arrives as a separate Create. Treat both as Delete
		// of the source — Create handles the destination side.
		w.publish(domain.SourceChange{Path: ev.Name, Op: domain.FileDeleted})
	case write:
		w.publish(domain.SourceChange{Path: ev.Name, Op: domain.FileModified})
	}
}

// handleNewDir walks a freshly-created directory: adds a watch for it
// (and every sub-dir not excluded) and emits FileCreated for every file
// already present. Necessary in case files were placed inside before our
// watch existed.
func (w *watcher) handleNewDir(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !w.allowed(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if err := w.addDir(path); err != nil {
				w.logger.Warn("watch new dir", "dir", path, "err", err)
			}
			return nil
		}
		w.publish(domain.SourceChange{Path: path, Op: domain.FileCreated})
		return nil
	})
}

// allowed reports whether path is inside the watched root, is not a dot
// entry, and does not match any exclude pattern.
func (w *watcher) allowed(path string) bool {
	rel, err := filepath.Rel(w.root, path)
	if err != nil || rel == "." {
		return false
	}
	if strings.HasPrefix(filepath.Base(path), ".") {
		return false
	}
	for _, p := range w.excludes {
		if ok, mErr := doublestar.PathMatch(p, rel); mErr == nil && ok {
			return false
		}
	}
	return true
}

func (w *watcher) publish(ev domain.SourceChange) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	subs := append([]chan domain.SourceChange(nil), w.subs...)
	w.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			w.logger.Warn("file event dropped (subscriber buffer full)",
				"path", ev.Path, "op", ev.Op.String())
		}
	}
}
