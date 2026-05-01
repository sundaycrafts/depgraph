// Package e2e contains end-to-end tests that spin up the HTTP server with
// real FS and mock analyzer adapters and exercise the full request path.
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	httpadapter "github.com/sundaycrafts/depgraph/internal/adapters/http"
	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/ports"
	"github.com/sundaycrafts/depgraph/gen"
)

// fixture creates a temp directory with two Go source files and returns its path.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n\nfunc main() {}\n")
	write("util.go", "package main\n\nfunc helper() {}\n")
	return root
}

// stubGraph is the graph returned by the mock analyzer.
func stubGraph(root string) domain.Graph {
	mainPath := filepath.Join(root, "main.go")
	utilPath := filepath.Join(root, "util.go")
	return domain.Graph{
		Nodes: []domain.Node{
			{ID: "n1", Kind: domain.NodeKindFile, Label: "main.go", Path: mainPath},
			{ID: "n2", Kind: domain.NodeKindFile, Label: "util.go", Path: utilPath},
			{ID: "n3", Kind: domain.NodeKindSymbol, Label: "main", Path: mainPath,
				Range: &domain.Range{
					Start: domain.Position{Line: 2, Character: 5},
					End:   domain.Position{Line: 2, Character: 9},
				}},
		},
		Edges: []domain.Edge{
			{ID: "e1", From: "n1", To: "n3", Kind: domain.EdgeKindDefines, Confidence: domain.ConfidenceExact},
		},
	}
}

// newTestServer wires up the HTTP adapter with a stub analyzer and real FS,
// returning a *httptest.Server backed by the real handler.
func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := fixture(t)
	graph := stubGraph(root)

	fsEditor := ports.EditorFunc(func(path string) (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	})

	adapter := httpadapter.New(graph, fsEditor)

	// Build the handler the same way Serve() does, but without starting a TCP listener.
	mux := http.NewServeMux()
	gen.HandlerWithOptions(adapter, gen.StdHTTPServerOptions{BaseRouter: mux})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, root
}

func TestGetGraph_ReturnsValidGraph(t *testing.T) {
	srv, root := newTestServer(t)

	resp, err := http.Get(srv.URL + "/graph")
	if err != nil {
		t.Fatalf("GET /graph: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type=%q, want application/json", ct)
	}

	var g gen.Graph
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(g.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(g.Edges))
	}

	// Verify file node has absolute path pointing to temp dir.
	var fileNode *gen.Node
	for i := range g.Nodes {
		if g.Nodes[i].Kind == gen.File {
			fileNode = &g.Nodes[i]
			break
		}
	}
	if fileNode == nil {
		t.Fatal("no file node found")
	}
	if fileNode.Path == nil || *fileNode.Path != filepath.Join(root, "main.go") {
		t.Errorf("file node path=%v, want %q", fileNode.Path, filepath.Join(root, "main.go"))
	}

	// Verify symbol node has range.
	var symNode *gen.Node
	for i := range g.Nodes {
		if g.Nodes[i].Kind == gen.Symbol {
			symNode = &g.Nodes[i]
			break
		}
	}
	if symNode == nil {
		t.Fatal("no symbol node found")
	}
	if symNode.Range == nil {
		t.Error("symbol node missing range")
	}
}

func TestGetGraph_EdgeKindsAndConfidence(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/graph")
	if err != nil {
		t.Fatalf("GET /graph: %v", err)
	}
	defer resp.Body.Close()

	var g gen.Graph
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatalf("decode: %v", err)
	}

	edge := g.Edges[0]
	if !edge.Kind.Valid() {
		t.Errorf("invalid edge kind: %q", edge.Kind)
	}
	if !edge.Confidence.Valid() {
		t.Errorf("invalid edge confidence: %q", edge.Confidence)
	}
	if edge.Kind != gen.Defines {
		t.Errorf("expected kind=defines, got %q", edge.Kind)
	}
	if edge.Confidence != gen.Exact {
		t.Errorf("expected confidence=exact, got %q", edge.Confidence)
	}
}

func TestGetFile_ReturnsContent(t *testing.T) {
	srv, root := newTestServer(t)

	mainPath := filepath.Join(root, "main.go")
	resp, err := http.Get(fmt.Sprintf("%s/file?path=%s", srv.URL, mainPath))
	if err != nil {
		t.Fatalf("GET /file: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var fc gen.FileContent
	if err := json.NewDecoder(resp.Body).Decode(&fc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fc.Path != mainPath {
		t.Errorf("path=%q, want %q", fc.Path, mainPath)
	}
	if fc.Content != "package main\n\nfunc main() {}\n" {
		t.Errorf("unexpected content: %q", fc.Content)
	}
}

func TestGetFile_NotFound(t *testing.T) {
	srv, root := newTestServer(t)

	missing := filepath.Join(root, "nonexistent.go")
	resp, err := http.Get(fmt.Sprintf("%s/file?path=%s", srv.URL, missing))
	if err != nil {
		t.Fatalf("GET /file: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	var errResp gen.Error
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestGetFile_MissingPathParam(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/file")
	if err != nil {
		t.Fatalf("GET /file (no param): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetFile_PathTraversal(t *testing.T) {
	srv, root := newTestServer(t)

	// Attempt to escape the root via path traversal.
	traversal := filepath.Join(root, "..", "etc", "passwd")
	resp, err := http.Get(fmt.Sprintf("%s/file?path=%s", srv.URL, traversal))
	if err != nil {
		t.Fatalf("GET /file: %v", err)
	}
	defer resp.Body.Close()

	// The FS adapter blocks traversal — expect 4xx, not 200.
	if resp.StatusCode == http.StatusOK {
		t.Errorf("path traversal returned 200, expected 4xx")
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	root := fixture(t)
	graph := stubGraph(root)

	fsEditor := ports.EditorFunc(func(path string) (string, error) {
		data, err := os.ReadFile(path)
		return string(data), err
	})

	// Use :0 to let the OS pick a free port.
	adapter := httpadapter.New(graph, fsEditor, httpadapter.WithAddr(":0"))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- adapter.Serve(ctx)
	}()

	// Cancel immediately; Serve() should return nil (not an error).
	cancel()
	if err := <-errCh; err != nil {
		t.Errorf("Serve returned error on graceful shutdown: %v", err)
	}
}
