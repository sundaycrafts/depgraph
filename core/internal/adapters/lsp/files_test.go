package lsp

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// makeTree creates the given files (relative to root, with empty content) and
// returns root.
func makeTree(t *testing.T, files []string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func relPaths(t *testing.T, root string, files []string) []string {
	t.Helper()
	out := make([]string, len(files))
	for i, f := range files {
		r, err := filepath.Rel(root, f)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = r
	}
	sort.Strings(out)
	return out
}

func TestFindFiles_NoExcludes(t *testing.T) {
	root := makeTree(t, []string{
		"main.go",
		"util_test.go",
		"internal/foo.go",
		"internal/foo_test.go",
		"README.md",
	})

	got, err := findFiles(root, []string{".go"}, nil)
	if err != nil {
		t.Fatalf("findFiles: %v", err)
	}
	gotRel := relPaths(t, root, got)
	want := []string{"internal/foo.go", "internal/foo_test.go", "main.go", "util_test.go"}
	if !equal(gotRel, want) {
		t.Errorf("got %v, want %v", gotRel, want)
	}
}

func TestFindFiles_ExcludeTestFiles(t *testing.T) {
	root := makeTree(t, []string{
		"main.go",
		"util_test.go",
		"internal/foo.go",
		"internal/foo_test.go",
	})

	got, err := findFiles(root, []string{".go"}, []string{"**/*_test.go"})
	if err != nil {
		t.Fatalf("findFiles: %v", err)
	}
	gotRel := relPaths(t, root, got)
	want := []string{"internal/foo.go", "main.go"}
	if !equal(gotRel, want) {
		t.Errorf("got %v, want %v", gotRel, want)
	}
}

func TestFindFiles_ExcludeDirGlob(t *testing.T) {
	root := makeTree(t, []string{
		"main.go",
		"vendor/dep/foo.go",
		"vendor/dep/bar.go",
		"internal/foo.go",
	})

	got, err := findFiles(root, []string{".go"}, []string{"vendor/**"})
	if err != nil {
		t.Fatalf("findFiles: %v", err)
	}
	gotRel := relPaths(t, root, got)
	want := []string{"internal/foo.go", "main.go"}
	if !equal(gotRel, want) {
		t.Errorf("got %v, want %v", gotRel, want)
	}
}

func TestFindFiles_MultipleExcludesOR(t *testing.T) {
	root := makeTree(t, []string{
		"main.go",
		"main_test.go",
		"vendor/dep/foo.go",
		"internal/foo.go",
	})

	got, err := findFiles(root, []string{".go"}, []string{"**/*_test.go", "vendor/**"})
	if err != nil {
		t.Fatalf("findFiles: %v", err)
	}
	gotRel := relPaths(t, root, got)
	want := []string{"internal/foo.go", "main.go"}
	if !equal(gotRel, want) {
		t.Errorf("got %v, want %v", gotRel, want)
	}
}

func TestFindFiles_DotEntriesAlwaysSkipped(t *testing.T) {
	root := makeTree(t, []string{
		"main.go",
		".git/HEAD",
		".git/objects/abc.go",
		".hidden/foo.go",
		".env.go",
	})

	got, err := findFiles(root, []string{".go"}, nil)
	if err != nil {
		t.Fatalf("findFiles: %v", err)
	}
	gotRel := relPaths(t, root, got)
	want := []string{"main.go"}
	if !equal(gotRel, want) {
		t.Errorf("got %v, want %v", gotRel, want)
	}
}

func TestFindFiles_DoublestarRecursive(t *testing.T) {
	root := makeTree(t, []string{
		"a/b/c/d/deep_test.go",
		"a/b/normal.go",
	})

	got, err := findFiles(root, []string{".go"}, []string{"**/*_test.go"})
	if err != nil {
		t.Fatalf("findFiles: %v", err)
	}
	gotRel := relPaths(t, root, got)
	want := []string{"a/b/normal.go"}
	if !equal(gotRel, want) {
		t.Errorf("got %v, want %v", gotRel, want)
	}
}

func TestFindFiles_InvalidPattern(t *testing.T) {
	root := makeTree(t, []string{"main.go"})
	_, err := findFiles(root, []string{".go"}, []string{"["})
	if err == nil {
		t.Fatal("expected error for invalid pattern")
	}
}

func equal(a, b []string) bool {
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
