package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/sundaycrafts/depgraph/internal/domain"
)

// stubEditor implements ports.EditorPort for tests.
type stubEditor struct{ files map[string]string }

func (s *stubEditor) GetFileContent(path string) (string, error) {
	if c, ok := s.files[path]; ok {
		return c, nil
	}
	return "", fmt.Errorf("file not found: %s", path)
}

// frame wraps a JSON body in Content-Length framing.
func frame(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

// serveOne runs the adapter, sends one request, reads one response, then cancels.
func serveOne(t *testing.T, a *Adapter, inW io.Writer, outR io.Reader, reqBody string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	// Scanner reads one response from out.
	scanner := bufio.NewScanner(outR)
	scanner.Buffer(make([]byte, 1*1024*1024), 1*1024*1024)
	scanner.Split(splitMCP)

	done := make(chan struct{})
	var resp map[string]any
	go func() {
		defer close(done)
		if scanner.Scan() {
			json.Unmarshal(scanner.Bytes(), &resp) //nolint:errcheck
		}
	}()

	go a.Serve(ctx) //nolint:errcheck

	fmt.Fprint(inW, frame(reqBody))

	<-done
	cancel()
	return resp
}

func TestSplitMCP(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{}}`
	framed := frame(body)
	adv, tok, err := splitMCP([]byte(framed), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adv != len(framed) {
		t.Errorf("advance=%d want %d", adv, len(framed))
	}
	if string(tok) != body {
		t.Errorf("token=%q want %q", tok, body)
	}
}

func TestSplitMCP_Partial(t *testing.T) {
	body := `{"jsonrpc":"2.0"}`
	framed := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body)+10, body)
	adv, tok, err := splitMCP([]byte(framed), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adv != 0 || tok != nil {
		t.Errorf("expected no progress, got advance=%d token=%q", adv, tok)
	}
}

func TestFindReferences(t *testing.T) {
	// Graph: A→B, C→B (direct references to B); D→A (indirect via A)
	graph := domain.Graph{
		Nodes: []domain.Node{
			{ID: "A", Kind: domain.NodeKindSymbol, Label: "FuncA"},
			{ID: "B", Kind: domain.NodeKindSymbol, Label: "FuncB"},
			{ID: "C", Kind: domain.NodeKindSymbol, Label: "FuncC"},
			{ID: "D", Kind: domain.NodeKindSymbol, Label: "FuncD"},
		},
		Edges: []domain.Edge{
			{ID: "e1", From: "A", To: "B", Kind: domain.EdgeKindReferences},
			{ID: "e2", From: "C", To: "B", Kind: domain.EdgeKindReferences},
			{ID: "e3", From: "D", To: "A", Kind: domain.EdgeKindReferences},
		},
	}
	a := newWithIO(graph, &stubEditor{}, &bytes.Buffer{}, &bytes.Buffer{})

	refs := a.findReferences("B")
	ids := make(map[string]bool)
	for _, n := range refs {
		ids[n.ID] = true
	}
	for _, want := range []string{"A", "C", "D"} {
		if !ids[want] {
			t.Errorf("expected %s in references of B, got %v", want, ids)
		}
	}
	if ids["B"] {
		t.Error("B should not appear in its own references")
	}
}

func TestServe_Initialize(t *testing.T) {
	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a := newWithIO(domain.Graph{}, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`,
	)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("unexpected protocolVersion: %v", result["protocolVersion"])
	}
}

func TestServe_ToolsList(t *testing.T) {
	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a := newWithIO(domain.Graph{}, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
	)

	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 4 {
		t.Errorf("expected 4 tools, got %d", len(tools))
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		m, _ := tool.(map[string]any)
		names = append(names, m["name"].(string))
	}
	for _, want := range []string{"root", "find_references", "find_symbols", "read_file"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tool %q not in list: %v", want, names)
		}
	}
}

func TestServe_FindReferences(t *testing.T) {
	graph := domain.Graph{
		Nodes: []domain.Node{
			{ID: "sym-A", Kind: domain.NodeKindSymbol, Label: "Alpha"},
			{ID: "sym-B", Kind: domain.NodeKindSymbol, Label: "Beta"},
		},
		Edges: []domain.Edge{
			{ID: "e1", From: "sym-A", To: "sym-B", Kind: domain.EdgeKindReferences},
		},
	}
	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a := newWithIO(graph, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_references","arguments":{"symbol_id":"sym-B"}}}`,
	)

	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("expected content in result")
	}
	item, _ := content[0].(map[string]any)
	text, _ := item["text"].(string)
	if !strings.Contains(text, "Alpha") {
		t.Errorf("expected Alpha in find_references result, got: %s", text)
	}
}

func TestFuzzyMatch(t *testing.T) {
	cases := []struct {
		query, target string
		want          bool
	}{
		{"add", "add", true},
		{"ad", "add", true},
		{"ADD", "add", true},
		{"add", "ADD", true},
		{"", "anything", true},
		{"", "", true},
		{"abc", "aXbXc", true},
		{"zzz", "add", false},
		{"addd", "add", false},
	}
	for _, tc := range cases {
		if got := fuzzyMatch(tc.query, tc.target); got != tc.want {
			t.Errorf("fuzzyMatch(%q, %q) = %v, want %v", tc.query, tc.target, got, tc.want)
		}
	}
}

func TestFindSymbols(t *testing.T) {
	graph := domain.Graph{
		Nodes: []domain.Node{
			{ID: "1", Kind: domain.NodeKindSymbol, Label: "add"},
			{ID: "2", Kind: domain.NodeKindSymbol, Label: "double"},
			{ID: "3", Kind: domain.NodeKindFile, Label: "lib.rs"},
		},
	}
	a := newWithIO(graph, &stubEditor{}, &bytes.Buffer{}, &bytes.Buffer{})

	assertIDs := func(t *testing.T, query string, wantIDs ...string) {
		t.Helper()
		nodes := a.findSymbols(query)
		got := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			got[n.ID] = true
		}
		for _, id := range wantIDs {
			if !got[id] {
				t.Errorf("query=%q: expected ID %q in result %v", query, id, got)
			}
		}
		if len(nodes) != len(wantIDs) {
			t.Errorf("query=%q: got %d results, want %d", query, len(nodes), len(wantIDs))
		}
	}

	assertIDs(t, "add", "1")
	assertIDs(t, "ad", "1")
	assertIDs(t, "ADD", "1")
	assertIDs(t, "dbl", "2")  // d→o→u→b→l matches subsequence
	assertIDs(t, "", "1", "2") // empty: all symbols (not files)
	assertIDs(t, "zzz")        // no match
}

func TestServe_FindSymbols(t *testing.T) {
	graph := domain.Graph{
		Nodes: []domain.Node{
			{ID: "sym-add", Kind: domain.NodeKindSymbol, Label: "add"},
			{ID: "sym-double", Kind: domain.NodeKindSymbol, Label: "double"},
		},
	}
	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a := newWithIO(graph, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_symbols","arguments":{"query":"ad"}}}`,
	)

	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("expected content in result")
	}
	item, _ := content[0].(map[string]any)
	text, _ := item["text"].(string)
	if !strings.Contains(text, "sym-add") {
		t.Errorf("expected sym-add in find_symbols result, got: %s", text)
	}
	if strings.Contains(text, "sym-double") {
		t.Errorf("sym-double should not match query 'ad', got: %s", text)
	}
}

func TestServe_UnknownMethod(t *testing.T) {
	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a := newWithIO(domain.Graph{}, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"unknown/method","params":{}}`,
	)

	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error in response, got: %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if int(code) != -32601 {
		t.Errorf("expected error code -32601, got %v", code)
	}
}
