package cache

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sundaycrafts/depgraph/internal/domain"
)

// stubAnalyzer implements ports.AnalyzerPort and counts invocations.
type stubAnalyzer struct {
	calls int
	graph domain.Graph
	err   error
}

func (s *stubAnalyzer) Analyze(_ context.Context, _ string) (domain.Graph, error) {
	s.calls++
	return s.graph, s.err
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestWrapper(t *testing.T, inner *stubAnalyzer, opts ...Option) *Wrapper {
	t.Helper()
	defaults := []Option{
		WithVersion("test"),
		WithDir(t.TempDir()),
		WithLogger(slog.New(slog.DiscardHandler)),
	}
	return New(inner, append(defaults, opts...)...)
}

func TestFingerprint_StableForUnchangedTree(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a")
	write(t, filepath.Join(root, "go.mod"), "module x")

	w := newTestWrapper(t, nil)
	fp1, err := w.computeFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := w.computeFingerprint(root)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Errorf("fingerprint should be stable: %s != %s", fp1, fp2)
	}
}

func TestFingerprint_ChangesOnFileEdit(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.go")
	write(t, file, "package a")

	w := newTestWrapper(t, nil)
	fp1, _ := w.computeFingerprint(root)

	time.Sleep(10 * time.Millisecond)
	write(t, file, "package a\nvar X = 1")

	fp2, _ := w.computeFingerprint(root)
	if fp1 == fp2 {
		t.Errorf("fingerprint should change after file edit; both = %s", fp1)
	}
}

// Binary version is intentionally NOT part of the fingerprint — invalidation
// across cache-format changes is handled by the schemaVersion field inside
// the cache file. Non-schema-changing releases must reuse existing caches.
func TestFingerprint_StableAcrossVersions(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a")

	fp1, _ := newTestWrapper(t, nil, WithVersion("v1")).computeFingerprint(root)
	fp2, _ := newTestWrapper(t, nil, WithVersion("v2")).computeFingerprint(root)
	if fp1 != fp2 {
		t.Errorf("fingerprint should be stable across binary versions: %s != %s", fp1, fp2)
	}
}

// User excludes that don't change the walked set (e.g. duplicates of default
// excludes) must NOT change the fingerprint. Otherwise an agent that
// inconsistently passes "node_modules/**" once and omits it the next time
// produces two different cache entries for the same source tree.
func TestFingerprint_StableAcrossRedundantExcludes(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.ts"), "export const x = 1")
	write(t, filepath.Join(root, "node_modules", "lib.ts"), "export const y = 1")

	fp1, _ := newTestWrapper(t, nil).computeFingerprint(root)
	fp2, _ := newTestWrapper(t, nil, WithExcludes([]string{"node_modules/**"})).computeFingerprint(root)
	if fp1 != fp2 {
		t.Errorf("fingerprint should be stable when user excludes are subsumed by defaults: %s vs %s", fp1, fp2)
	}
}

func TestFingerprint_ChangesOnExcludes(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a")
	write(t, filepath.Join(root, "ignored", "b.go"), "package b")

	fp1, _ := newTestWrapper(t, nil).computeFingerprint(root)
	fp2, _ := newTestWrapper(t, nil, WithExcludes([]string{"ignored/**"})).computeFingerprint(root)
	if fp1 == fp2 {
		t.Errorf("fingerprint should change when excludes change the walked set")
	}
}

func TestFingerprint_DefaultExcludesAreApplied(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.ts"), "export const x = 1")
	// node_modules should be excluded by DefaultExcludes for TypeScript.
	write(t, filepath.Join(root, "node_modules", "lib.ts"), "export const y = 1")

	fp1, _ := newTestWrapper(t, nil).computeFingerprint(root)

	// Add another file inside node_modules — should not change the fingerprint.
	write(t, filepath.Join(root, "node_modules", "another.ts"), "export const z = 1")
	fp2, _ := newTestWrapper(t, nil).computeFingerprint(root)
	if fp1 != fp2 {
		t.Errorf("changes inside node_modules should not affect fingerprint: %s vs %s", fp1, fp2)
	}
}

func TestFingerprint_IgnoresIrrelevantFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a")

	fp1, _ := newTestWrapper(t, nil).computeFingerprint(root)

	// README.md is not a known source ext or marker file; should be ignored.
	write(t, filepath.Join(root, "README.md"), "docs")
	fp2, _ := newTestWrapper(t, nil).computeFingerprint(root)
	if fp1 != fp2 {
		t.Errorf("non-source file changes should not affect fingerprint")
	}
}

func TestWrapper_HitOnSecondCall(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a")

	stub := &stubAnalyzer{graph: domain.Graph{Nodes: []domain.Node{{ID: "n1", Label: "A"}}}}
	w := newTestWrapper(t, stub)

	g1, err := w.Analyze(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := w.Analyze(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	if stub.calls != 1 {
		t.Errorf("expected inner analyzer called once, got %d", stub.calls)
	}
	if len(g1.Nodes) != 1 || len(g2.Nodes) != 1 || g2.Nodes[0].ID != "n1" {
		t.Errorf("cached graph mismatch: g1=%+v g2=%+v", g1, g2)
	}
}

func TestWrapper_MissAfterFileEdit(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.go")
	write(t, file, "package a")

	stub := &stubAnalyzer{graph: domain.Graph{Nodes: []domain.Node{{ID: "n1"}}}}
	w := newTestWrapper(t, stub)

	if _, err := w.Analyze(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	write(t, file, "package b")

	if _, err := w.Analyze(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	if stub.calls != 2 {
		t.Errorf("expected 2 inner calls after edit, got %d", stub.calls)
	}
}

func TestLoadGraph_SchemaMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fp.json")
	want := domain.Graph{Nodes: []domain.Node{{ID: "n1", Label: "Alpha"}}}

	if err := saveGraph(path, want); err != nil {
		t.Fatalf("saveGraph: %v", err)
	}
	got, err := loadGraph(path)
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].ID != "n1" {
		t.Errorf("graph round-trip mismatch: got %+v", got)
	}
}

func TestLoadGraph_SchemaMismatch_DeletesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fp.json")
	// Write a stale entry with an old schema version directly to disk.
	stale := `{"schemaVersion":"2025-01-01","graph":{"Nodes":[{"ID":"old"}]}}`
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadGraph(path); err == nil {
		t.Fatal("expected error for stale schema, got nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale cache file should be deleted; stat err=%v", err)
	}
}

func TestLoadGraph_NoSchemaVersion_DeletesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fp.json")
	// Pre-feature cache files have no schemaVersion field at all (zero value "").
	legacy := `{"Nodes":[{"ID":"old"}],"Edges":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadGraph(path); err == nil {
		t.Fatal("expected error for legacy cache, got nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("legacy cache file should be deleted; stat err=%v", err)
	}
}

func TestWrapper_DisabledAlwaysCalls(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a")

	stub := &stubAnalyzer{graph: domain.Graph{}}
	w := newTestWrapper(t, stub, WithDisabled())

	for i := 0; i < 3; i++ {
		if _, err := w.Analyze(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
	if stub.calls != 3 {
		t.Errorf("disabled cache should always invoke inner; got %d calls", stub.calls)
	}
}
