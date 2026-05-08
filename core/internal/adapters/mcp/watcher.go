package mcp

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
)

// FileOp categorises a filesystem change observed under a component root.
type FileOp int

const (
	FileCreated FileOp = iota
	FileModified
	FileDeleted
)

func (o FileOp) String() string {
	switch o {
	case FileCreated:
		return "create"
	case FileModified:
		return "modify"
	case FileDeleted:
		return "delete"
	}
	return "?"
}

// FileEvent describes a change observed under a component root. Path is the
// absolute filesystem path; subscribers map it to a file:// URI themselves
// because the URI scheme is LSP-specific.
type FileEvent struct {
	Path string
	Op   FileOp
}

// fileEventSource is the surface session-side subscribers use. Tests
// substitute fakes that push events directly without touching fsnotify.
type fileEventSource interface {
	Subscribe() <-chan FileEvent
	Stop()
}

// FileWatcher monitors a project root via fsnotify and fans out events
// to multiple subscribers. Directory watches are added recursively at
// construction; subdirectories created at runtime are picked up via the
// CREATE event handler.
//
// Drop-on-full semantics: if a subscriber's buffer is full we drop the
// event and warn. Subsequent Modify events are idempotent (TS subscriber
// resends the full file content) so a single dropped Modify recovers on
// the next save; a dropped Create/Delete is rare enough to accept.
type FileWatcher struct {
	root     string
	excludes []string
	logger   *slog.Logger

	fsw *fsnotify.Watcher

	mu      sync.Mutex
	subs    []chan FileEvent
	watched map[string]bool
	closed  bool
	done    chan struct{}
}

// NewFileWatcher walks root respecting excludes (using the same dot-skip
// and doublestar rules as findSourceFiles), adds an fsnotify watch for
// every visited directory, and starts a fan-out goroutine. Failures —
// including hitting the inotify per-process limit — are returned so the
// component can refuse to register; running with a stale view is worse
// than refusing to start because the agent silently sees old results.
func NewFileWatcher(root string, excludes []string, logger *slog.Logger) (*FileWatcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	w := &FileWatcher{
		root:     root,
		excludes: append([]string(nil), excludes...),
		logger:   logger.With("watcher", root),
		fsw:      fsw,
		watched:  make(map[string]bool),
		done:     make(chan struct{}),
	}
	dirs, err := findWatchedDirs(root, w.excludes)
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

// Subscribe returns a buffered channel that receives every event observed
// after the call. The returned channel is closed when Stop is called; new
// subscribes after Stop receive a closed channel immediately.
func (w *FileWatcher) Subscribe() <-chan FileEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := make(chan FileEvent, 64)
	if w.closed {
		close(ch)
		return ch
	}
	w.subs = append(w.subs, ch)
	return ch
}

// Stop closes the underlying fsnotify watcher, waits for the run loop to
// exit, then closes every subscriber channel. Safe to call multiple times.
func (w *FileWatcher) Stop() {
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

func (w *FileWatcher) addDir(dir string) error {
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

func (w *FileWatcher) run() {
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
func (w *FileWatcher) handle(ev fsnotify.Event) {
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
		w.publish(FileEvent{Path: ev.Name, Op: FileCreated})
	case remove, rename:
		// fsnotify emits Rename for the source path of a rename; the
		// destination arrives as a separate Create. Treat both as Delete
		// of the source — Create handles the destination side.
		w.publish(FileEvent{Path: ev.Name, Op: FileDeleted})
	case write:
		w.publish(FileEvent{Path: ev.Name, Op: FileModified})
	}
}

// handleNewDir walks a freshly-created directory: adds a watch for it
// (and every sub-dir not excluded) and emits FileCreated for every file
// already present. We must do this in case files were placed inside
// before our watch existed.
func (w *FileWatcher) handleNewDir(dir string) {
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
		w.publish(FileEvent{Path: path, Op: FileCreated})
		return nil
	})
}

// allowed reports whether path is inside the watched root, is not a dot
// entry, and does not match any exclude pattern.
func (w *FileWatcher) allowed(path string) bool {
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

func (w *FileWatcher) publish(ev FileEvent) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	subs := append([]chan FileEvent(nil), w.subs...)
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

// findWatchedDirs walks root collecting every directory not skipped by the
// dot-prefix rule or the exclude patterns. The traversal mirrors
// findSourceFiles so the watcher's view stays in sync with the tool
// handlers' view of "what belongs to this component".
func findWatchedDirs(root string, excludes []string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." {
			for _, p := range excludes {
				ok, mErr := doublestar.PathMatch(p, rel)
				if mErr != nil {
					return fmt.Errorf("invalid exclude pattern %q: %w", p, mErr)
				}
				if ok {
					return filepath.SkipDir
				}
			}
		}
		dirs = append(dirs, path)
		return nil
	})
	return dirs, err
}
