package domain

// PortSourceWatcher observes changes under a project root and fans them
// out as SourceChange events. The current production implementation is
// fsnotify-based (`adapters/fswatch`), but the interface is independent
// of any specific watcher technology.
//
// Implementations must be safe for concurrent Subscribe / Stop calls.
type PortSourceWatcher interface {
	// Subscribe returns a buffered channel that receives every change
	// observed after the call. Closed when Stop is called; subscribes
	// after Stop receive a closed channel immediately.
	Subscribe() <-chan SourceChange

	// Stop closes the underlying watcher and every subscriber channel.
	// Safe to call multiple times.
	Stop()
}

// PortSourceWatcherFactory builds a PortSourceWatcher for the given
// root. The excludes slice is doublestar-style and matched against paths
// relative to root; matched directories are not watched.
//
// Construction errors (e.g. inotify limit reached) are returned to the
// caller — the workspace refuses to register a project whose disk view
// cannot be kept in sync.
type PortSourceWatcherFactory interface {
	Watch(root string, excludes []string) (PortSourceWatcher, error)
}
