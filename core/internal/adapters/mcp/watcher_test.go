package mcp

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestFindWatchedDirs_Basic(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))
	mustMkdir(t, filepath.Join(root, "src", "sub"))
	mustMkdir(t, filepath.Join(root, "vendor"))
	mustMkdir(t, filepath.Join(root, ".git"))

	dirs, err := findWatchedDirs(root, []string{"vendor/**"})
	if err != nil {
		t.Fatalf("findWatchedDirs: %v", err)
	}
	got := relPaths(t, root, dirs)
	want := []string{".", "src", "src/sub"}
	sort.Strings(got)
	sort.Strings(want)
	if !equalSlices(got, want) {
		t.Errorf("dirs = %v, want %v", got, want)
	}
}

func TestFindWatchedDirs_InvalidPattern(t *testing.T) {
	root := t.TempDir()
	// findWatchedDirs only consults excludes for subdirs; ensure at least
	// one subdir exists so the pattern is actually tested.
	mustMkdir(t, filepath.Join(root, "anydir"))
	if _, err := findWatchedDirs(root, []string{"foo[unbalanced"}); err == nil {
		t.Fatal("expected error for invalid glob, got nil")
	}
}

func TestFileWatcher_CreateModifyDelete(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))

	w, err := NewFileWatcher(root, nil, slog.Default())
	if err != nil {
		t.Fatalf("NewFileWatcher: %v", err)
	}
	defer w.Stop()
	events := w.Subscribe()

	file := filepath.Join(root, "src", "a.ts")
	if err := os.WriteFile(file, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, file, FileCreated)

	if err := os.WriteFile(file, []byte("alpha-2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// fsnotify can collapse the rewrite into Create+Write; accept either Create or Modify.
	expectEventAny(t, events, file, []FileOp{FileModified, FileCreated})

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, file, FileDeleted)
}

func TestFileWatcher_NewSubdirectoryAddsWatch(t *testing.T) {
	root := t.TempDir()

	w, err := NewFileWatcher(root, nil, slog.Default())
	if err != nil {
		t.Fatalf("NewFileWatcher: %v", err)
	}
	defer w.Stop()
	events := w.Subscribe()

	subdir := filepath.Join(root, "fresh")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(subdir, "x.ts")
	// Give the watcher a brief moment to install the watch on the new dir
	// before we create the file inside it. Without this, the file creation
	// races the watch addition. fsnotify itself does not require this on
	// every platform, but the test must be deterministic.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, file, FileCreated)
}

func TestFileWatcher_ExcludedPathsIgnored(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "node_modules"))

	w, err := NewFileWatcher(root, []string{"node_modules/**"}, slog.Default())
	if err != nil {
		t.Fatalf("NewFileWatcher: %v", err)
	}
	defer w.Stop()
	events := w.Subscribe()

	if err := os.WriteFile(filepath.Join(root, "node_modules", "noisy.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure no event arrives in a small window.
	select {
	case ev := <-events:
		t.Fatalf("unexpected event for excluded path: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestFileWatcher_StopClosesSubscribers(t *testing.T) {
	root := t.TempDir()
	w, err := NewFileWatcher(root, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	events := w.Subscribe()
	w.Stop()

	select {
	case _, ok := <-events:
		if ok {
			t.Error("expected channel to be closed, got value")
		}
	case <-time.After(time.Second):
		t.Error("subscriber channel was not closed within timeout")
	}
}

// --- helpers ---------------------------------------------------------------

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func relPaths(t *testing.T, root string, paths []string) []string {
	t.Helper()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rel)
	}
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// expectEvent blocks for up to 2s waiting for an event matching path+op.
// Other events (e.g. fsnotify CHMOD) are skipped.
func expectEvent(t *testing.T, events <-chan FileEvent, path string, op FileOp) {
	t.Helper()
	expectEventAny(t, events, path, []FileOp{op})
}

func expectEventAny(t *testing.T, events <-chan FileEvent, path string, allowed []FileOp) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Path != path {
				continue
			}
			for _, op := range allowed {
				if ev.Op == op {
					return
				}
			}
			// matched path but unexpected op — keep reading; OS may emit
			// CHMOD-style events that we currently ignore upstream.
		case <-deadline:
			t.Fatalf("timed out waiting for event %q with op in %v", path, allowed)
		}
	}
}
