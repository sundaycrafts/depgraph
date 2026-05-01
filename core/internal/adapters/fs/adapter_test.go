package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	fsadapter "github.com/sundaycrafts/depgraph/internal/adapters/fs"
)

func TestGetFileContent(t *testing.T) {
	root := t.TempDir()

	content := "package main\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "util.go"), []byte("package sub\n"), 0644); err != nil {
		t.Fatal(err)
	}

	a := fsadapter.New(root)

	t.Run("returns content of file inside root", func(t *testing.T) {
		got, err := a.GetFileContent(filepath.Join(root, "main.go"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != content {
			t.Errorf("got %q, want %q", got, content)
		}
	})

	t.Run("returns content of file in subdirectory", func(t *testing.T) {
		got, err := a.GetFileContent(filepath.Join(root, "sub", "util.go"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "package sub\n" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("path traversal returns error", func(t *testing.T) {
		_, err := a.GetFileContent(filepath.Join(root, "..", "etc", "passwd"))
		if err == nil {
			t.Error("expected error for path traversal, got nil")
		}
	})

	t.Run("non-existent file returns os.ErrNotExist", func(t *testing.T) {
		_, err := a.GetFileContent(filepath.Join(root, "nonexistent.go"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expected os.ErrNotExist, got %v", err)
		}
	})
}
