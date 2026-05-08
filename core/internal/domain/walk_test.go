package domain

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestWalkDirectories_Basic(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))
	mustMkdir(t, filepath.Join(root, "src", "sub"))
	mustMkdir(t, filepath.Join(root, "vendor"))
	mustMkdir(t, filepath.Join(root, ".git"))

	dirs, err := WalkDirectories(root, []string{"vendor/**"})
	if err != nil {
		t.Fatalf("WalkDirectories: %v", err)
	}
	got := relPaths(t, root, dirs)
	want := []string{".", "src", "src/sub"}
	sort.Strings(got)
	sort.Strings(want)
	if !equalSlices(got, want) {
		t.Errorf("dirs = %v, want %v", got, want)
	}
}

func TestWalkDirectories_InvalidPattern(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "anydir"))
	if _, err := WalkDirectories(root, []string{"foo[unbalanced"}); err == nil {
		t.Fatal("expected error for invalid glob, got nil")
	}
}

func TestWalkSourceFiles_FiltersByExt(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "b.txt"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := WalkSourceFiles(root, []string{".go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "a.go" {
		t.Errorf("files=%v, want only a.go", files)
	}
}

// --- helpers shared with the watcher tests once they live elsewhere ---

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
