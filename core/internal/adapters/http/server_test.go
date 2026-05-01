package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sundaycrafts/depgraph/gen"
	httpadapter "github.com/sundaycrafts/depgraph/internal/adapters/http"
	"github.com/sundaycrafts/depgraph/internal/domain"
)

type mockEditor struct {
	files map[string]string
}

func (m *mockEditor) GetFileContent(path string) (string, error) {
	content, ok := m.files[path]
	if !ok {
		return "", os.ErrNotExist
	}
	return content, nil
}

func newTestAdapter(graph domain.Graph, files map[string]string) *httpadapter.Adapter {
	return httpadapter.New(graph, &mockEditor{files: files})
}

func TestGetGraph(t *testing.T) {
	path := "/src/main.go"
	graph := domain.Graph{
		Nodes: []domain.Node{
			{ID: "n1", Kind: domain.NodeKindFile, Label: "main.go", Path: path},
		},
		Edges: []domain.Edge{
			{ID: "e1", From: "n1", To: "n1", Kind: domain.EdgeKindDefines, Confidence: domain.ConfidenceExact},
		},
	}

	a := newTestAdapter(graph, nil)
	handler := gen.Handler(a)

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var got gen.Graph
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Id != "n1" {
		t.Errorf("unexpected nodes: %+v", got.Nodes)
	}
	if len(got.Edges) != 1 || got.Edges[0].Id != "e1" {
		t.Errorf("unexpected edges: %+v", got.Edges)
	}
	if got.Nodes[0].Path == nil || *got.Nodes[0].Path != path {
		t.Errorf("expected path %q, got %v", path, got.Nodes[0].Path)
	}
}

func TestGetFile(t *testing.T) {
	files := map[string]string{
		"/src/main.go": "package main\n",
	}
	a := newTestAdapter(domain.Graph{}, files)
	handler := gen.Handler(a)

	t.Run("returns file content", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/file?path=/src/main.go", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var got gen.FileContent
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}
		if got.Content != "package main\n" {
			t.Errorf("unexpected content: %q", got.Content)
		}
	})

	t.Run("returns 404 for missing file", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/file?path=/src/missing.go", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})

	t.Run("returns 400 when path param missing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/file", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", w.Code)
		}
	})
}
