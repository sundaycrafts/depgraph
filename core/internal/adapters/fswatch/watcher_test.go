package fswatch

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sundaycrafts/depgraph/internal/domain"
)

func TestWatcher_CreateModifyDelete(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))

	w, err := newWatcher(root, nil, slog.Default())
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}
	defer w.Stop()
	events := w.Subscribe()

	file := filepath.Join(root, "src", "a.ts")
	if err := os.WriteFile(file, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, file, domain.FileCreated)

	if err := os.WriteFile(file, []byte("alpha-2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// fsnotify can collapse the rewrite into Create+Write; accept either.
	expectEventAny(t, events, file, []domain.FileOp{domain.FileModified, domain.FileCreated})

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, file, domain.FileDeleted)
}

func TestWatcher_NewSubdirectoryAddsWatch(t *testing.T) {
	root := t.TempDir()

	w, err := newWatcher(root, nil, slog.Default())
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}
	defer w.Stop()
	events := w.Subscribe()

	subdir := filepath.Join(root, "fresh")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(subdir, "x.ts")
	// Give the watcher a brief moment to install the watch on the new
	// dir before we create the file inside it.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, file, domain.FileCreated)
}

func TestWatcher_ExcludedPathsIgnored(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "node_modules"))

	w, err := newWatcher(root, []string{"node_modules/**"}, slog.Default())
	if err != nil {
		t.Fatalf("newWatcher: %v", err)
	}
	defer w.Stop()
	events := w.Subscribe()

	if err := os.WriteFile(filepath.Join(root, "node_modules", "noisy.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("unexpected event for excluded path: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWatcher_StopClosesSubscribers(t *testing.T) {
	root := t.TempDir()
	w, err := newWatcher(root, nil, slog.Default())
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

func expectEvent(t *testing.T, events <-chan domain.SourceChange, path string, op domain.FileOp) {
	t.Helper()
	expectEventAny(t, events, path, []domain.FileOp{op})
}

func expectEventAny(t *testing.T, events <-chan domain.SourceChange, path string, allowed []domain.FileOp) {
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
		case <-deadline:
			t.Fatalf("timed out waiting for event %q with op in %v", path, allowed)
		}
	}
}
