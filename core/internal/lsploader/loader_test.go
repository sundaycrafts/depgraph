package lsploader_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// stubLocator maps binary names to results.
type stubLocator map[string]string

func (s stubLocator) LookupBinary(name string) (string, error) {
	if path, ok := s[name]; ok {
		return path, nil
	}
	return "", errors.New("not found")
}

func TestDetect_Go(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0644)

	langs, err := lsploader.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(langs) != 1 || langs[0] != lsploader.Go {
		t.Errorf("got %v, want [go]", langs)
	}
}

func TestDetect_Rust(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0644)

	langs, err := lsploader.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(langs) != 1 || langs[0] != lsploader.Rust {
		t.Errorf("got %v, want [rust]", langs)
	}
}

func TestDetect_TypeScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{}\n"), 0644)

	langs, err := lsploader.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(langs) != 1 || langs[0] != lsploader.TypeScript {
		t.Errorf("got %v, want [typescript]", langs)
	}
}

func TestDetect_MultiLanguage(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0644)
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0644)

	langs, err := lsploader.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(langs) != 2 {
		t.Errorf("got %d languages, want 2: %v", len(langs), langs)
	}
	// canonical order: go before rust
	if langs[0] != lsploader.Go || langs[1] != lsploader.Rust {
		t.Errorf("order wrong: %v", langs)
	}
}

func TestDetect_Empty(t *testing.T) {
	dir := t.TempDir()
	langs, err := lsploader.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(langs) != 0 {
		t.Errorf("expected no languages, got %v", langs)
	}
}

func TestCheck_AllPresent(t *testing.T) {
	loc := stubLocator{
		"gopls":                        "/usr/bin/gopls",
		"rust-analyzer":                "/usr/bin/rust-analyzer",
		"typescript-language-server":   "/usr/bin/typescript-language-server",
	}
	langs := []lsploader.Language{lsploader.Go, lsploader.Rust}
	if err := lsploader.Check(loc, langs); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestCheck_MissingOne(t *testing.T) {
	loc := stubLocator{"gopls": "/usr/bin/gopls"}
	langs := []lsploader.Language{lsploader.Go, lsploader.Rust}

	err := lsploader.Check(loc, langs)
	if err == nil {
		t.Fatal("expected error for missing rust-analyzer")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rust-analyzer") {
		t.Errorf("error missing binary name: %q", msg)
	}
	if !strings.Contains(msg, "rustup component add rust-analyzer") {
		t.Errorf("error missing install hint: %q", msg)
	}
}

func TestCheck_MissingMultiple(t *testing.T) {
	loc := stubLocator{} // nothing found
	langs := []lsploader.Language{lsploader.Go, lsploader.TypeScript}

	err := lsploader.Check(loc, langs)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "gopls") || !strings.Contains(msg, "typescript-language-server") {
		t.Errorf("error should list all missing binaries: %q", msg)
	}
}
