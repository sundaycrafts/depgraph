package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sundaycrafts/depgraph/internal/config"
)

// writeFile writes data to path, creating parents as needed.
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_FileAbsent(t *testing.T) {
	cwd := t.TempDir()
	cfg, warnings := config.Load(cwd)
	if cfg != nil {
		t.Errorf("expected cfg=nil for missing file, got %+v", cfg)
	}
	if warnings != nil {
		t.Errorf("expected no warnings for missing file, got %v", warnings)
	}
}

func TestLoad_HappyPath(t *testing.T) {
	cwd := t.TempDir()
	subdir := filepath.Join(cwd, "service")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cwd, "depgraph.yaml"), `
projects:
  - root: ./service
    excludes:
      - "**/*.test.ts"
    exclude_symbols:
      - getServerSideProps
      - function:getStaticPaths
`)

	cfg, warnings := config.Load(cwd)
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if cfg == nil {
		t.Fatal("expected cfg, got nil")
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(cfg.Projects))
	}
	got := cfg.Projects[0]
	if got.Root != subdir {
		t.Errorf("Root = %q, want %q (resolved absolute)", got.Root, subdir)
	}
	if len(got.Excludes) != 1 || got.Excludes[0] != "**/*.test.ts" {
		t.Errorf("Excludes = %v", got.Excludes)
	}
	if len(got.ExcludeSymbols) != 2 {
		t.Errorf("ExcludeSymbols = %v", got.ExcludeSymbols)
	}
}

func TestLoad_AbsoluteAndRelativeRoots(t *testing.T) {
	cwd := t.TempDir()
	abs := t.TempDir()
	rel := filepath.Join(cwd, "rel")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cwd, "depgraph.yaml"), `
projects:
  - root: `+abs+`
  - root: ./rel
`)

	cfg, warnings := config.Load(cwd)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if cfg == nil || len(cfg.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %+v", cfg)
	}
	if cfg.Projects[0].Root != abs {
		t.Errorf("project[0].Root = %q want %q", cfg.Projects[0].Root, abs)
	}
	if cfg.Projects[1].Root != rel {
		t.Errorf("project[1].Root = %q want %q", cfg.Projects[1].Root, rel)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "depgraph.yaml"), "projects: not-an-array\n")
	cfg, warnings := config.Load(cwd)
	if cfg != nil {
		t.Errorf("expected cfg=nil for malformed YAML, got %+v", cfg)
	}
	if len(warnings) == 0 {
		t.Errorf("expected at least one warning for malformed YAML")
	}
	if !containsSubstring(warnings, "depgraph.yaml") {
		t.Errorf("warning should reference filename: %v", warnings)
	}
}

func TestLoad_PartialInvalid(t *testing.T) {
	cwd := t.TempDir()
	good := filepath.Join(cwd, "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(cwd, "does-not-exist")
	writeFile(t, filepath.Join(cwd, "depgraph.yaml"), `
projects:
  - root: ""
  - root: `+missing+`
  - root: `+good+`
`)

	cfg, warnings := config.Load(cwd)
	if cfg == nil {
		t.Fatal("expected partial cfg, got nil")
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 valid project (the rest dropped), got %d: %+v", len(cfg.Projects), cfg.Projects)
	}
	if cfg.Projects[0].Root != good {
		t.Errorf("surviving project root = %q want %q", cfg.Projects[0].Root, good)
	}
	if len(warnings) != 2 {
		t.Errorf("expected 2 warnings (empty root + missing dir), got %d: %v", len(warnings), warnings)
	}
}

func TestLoad_RootIsFile(t *testing.T) {
	cwd := t.TempDir()
	asFile := filepath.Join(cwd, "iam-a-file.txt")
	writeFile(t, asFile, "")
	writeFile(t, filepath.Join(cwd, "depgraph.yaml"), `
projects:
  - root: `+asFile+`
`)
	cfg, warnings := config.Load(cwd)
	if cfg == nil {
		t.Fatal("expected cfg")
	}
	if len(cfg.Projects) != 0 {
		t.Errorf("file root should have been dropped, got %+v", cfg.Projects)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not a directory") {
		t.Errorf("expected single 'not a directory' warning, got %v", warnings)
	}
}

func containsSubstring(strs []string, sub string) bool {
	for _, s := range strs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
