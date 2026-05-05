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
	"time"

	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/ports"
)

// stubEditor implements ports.EditorPort for tests.
type stubEditor struct{ files map[string]string }

func (s *stubEditor) GetFileContent(path string) (string, error) {
	if c, ok := s.files[path]; ok {
		return c, nil
	}
	return "", fmt.Errorf("file not found: %s", path)
}

// frame wraps a JSON body in newline-delimited format (MCP 2025-11-25 stdio transport).
func frame(body string) string {
	return body + "\n"
}

// serveOne runs the adapter, sends one request, reads one response, then cancels.
func serveOne(t *testing.T, a *Adapter, inW io.Writer, outR io.Reader, reqBody string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	// Scanner reads one response from out.
	scanner := bufio.NewScanner(outR)
	scanner.Buffer(make([]byte, 1*1024*1024), 1*1024*1024)
	scanner.Split(splitJSONLines)

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

func TestSplitJSONLines(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{}}`
	framed := frame(body)
	adv, tok, err := splitJSONLines([]byte(framed), false)
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

func TestSplitJSONLines_Partial(t *testing.T) {
	// Partial message without trailing newline and not at EOF → no progress.
	body := `{"jsonrpc":"2.0"}`
	adv, tok, err := splitJSONLines([]byte(body), false)
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
	a := newWithIO("/", graph, &stubEditor{}, &bytes.Buffer{}, &bytes.Buffer{})
	entry := a.analyses["/"]

	refs := a.findReferences(entry, "B")
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
	a := newWithIO("/", domain.Graph{}, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`,
	)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" {
		t.Errorf("unexpected protocolVersion: %v", result["protocolVersion"])
	}
}

func TestServe_ToolsList(t *testing.T) {
	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a := newWithIO("/", domain.Graph{}, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
	)

	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(tools))
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		m, _ := tool.(map[string]any)
		names = append(names, m["name"].(string))
	}
	for _, want := range []string{"warmup", "find_references", "find_symbols"} {
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
	a := newWithIO("/", graph, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_references","arguments":{"root":"/","symbol_id":"sym-B"}}}`,
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
	a := newWithIO("/", graph, &stubEditor{}, &bytes.Buffer{}, &bytes.Buffer{})
	entry := a.analyses["/"]

	assertIDs := func(t *testing.T, query string, wantIDs ...string) {
		t.Helper()
		nodes := a.findSymbols(entry, query)
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
	assertIDs(t, "dbl", "2")   // d→o→u→b→l matches subsequence
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
	a := newWithIO("/", graph, &stubEditor{}, inPR, outPW)

	resp := serveOne(t, a, inPW, outPR,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/","query":"ad"}}}`,
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

// blockingAnalyzer blocks until release is closed, then returns the given graph.
type blockingAnalyzer struct {
	graph   domain.Graph
	release chan struct{}
}

func (b *blockingAnalyzer) Analyze(ctx context.Context, root string) (domain.Graph, error) {
	select {
	case <-b.release:
		return b.graph, nil
	case <-ctx.Done():
		return domain.Graph{}, ctx.Err()
	}
}

func TestServe_Warmup_Async(t *testing.T) {
	release := make(chan struct{})
	stub := &blockingAnalyzer{
		graph: domain.Graph{
			Nodes: []domain.Node{{ID: "sym-1", Kind: domain.NodeKindSymbol, Label: "MyFunc"}},
		},
		release: release,
	}

	a := New(
		func(excludes []string) ports.AnalyzerPort { return stub },
		func(root string) ports.EditorPort { return &stubEditor{} },
	)

	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a.in = inPR
	a.out = outPW

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go a.Serve(ctx) //nolint:errcheck

	send := func(body string) map[string]any {
		t.Helper()
		fmt.Fprint(inPW, frame(body))
		scanner := bufio.NewScanner(outPR)
		scanner.Buffer(make([]byte, 1*1024*1024), 1*1024*1024)
		scanner.Split(splitJSONLines)
		var resp map[string]any
		if scanner.Scan() {
			json.Unmarshal(scanner.Bytes(), &resp) //nolint:errcheck
		}
		return resp
	}

	// warmup must return immediately with status:warming_up
	warmupResp := send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"warmup","arguments":{"root":"/tmp"}}}`)
	if warmupResp["error"] != nil {
		t.Fatalf("warmup returned error: %v", warmupResp["error"])
	}
	result, _ := warmupResp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	item, _ := content[0].(map[string]any)
	if item["text"] != `{"status":"warming_up"}` {
		t.Errorf("expected warming_up status, got: %v", item["text"])
	}

	// find_symbols while warmup is in progress must return "retry shortly" error
	findResp := send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/tmp","query":""}}}`)
	errObj, ok := findResp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error while warming up, got result: %v", findResp)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "retry shortly") {
		t.Errorf("expected retry-shortly message, got: %s", msg)
	}

	// unblock the analyzer
	close(release)

	// poll until stateReady (give the goroutine time to finish)
	var readyResp map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		readyResp = send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/tmp","query":""}}}`)
		if readyResp["error"] == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if readyResp["error"] != nil {
		t.Fatalf("find_symbols still failing after warmup completed: %v", readyResp["error"])
	}
	result2, _ := readyResp["result"].(map[string]any)
	content2, _ := result2["content"].([]any)
	item2, _ := content2[0].(map[string]any)
	if !strings.Contains(item2["text"].(string), "MyFunc") {
		t.Errorf("expected MyFunc in find_symbols result, got: %v", item2["text"])
	}
}

func TestServe_Warmup_MultiRoot(t *testing.T) {
	backendRelease := make(chan struct{})
	frontendRelease := make(chan struct{})

	backendStub := &blockingAnalyzer{
		graph: domain.Graph{
			Nodes: []domain.Node{{ID: "go-1", Kind: domain.NodeKindSymbol, Label: "GoHandler"}},
		},
		release: backendRelease,
	}
	frontendStub := &blockingAnalyzer{
		graph: domain.Graph{
			Nodes: []domain.Node{{ID: "ts-1", Kind: domain.NodeKindSymbol, Label: "TsComponent"}},
		},
		release: frontendRelease,
	}

	stubs := make(chan ports.AnalyzerPort, 2)
	stubs <- backendStub
	stubs <- frontendStub

	a := New(
		func(excludes []string) ports.AnalyzerPort { return <-stubs },
		func(root string) ports.EditorPort { return &stubEditor{} },
	)

	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a.in = inPR
	a.out = outPW

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go a.Serve(ctx) //nolint:errcheck

	msgID := 0
	send := func(body string) map[string]any {
		t.Helper()
		fmt.Fprint(inPW, frame(body))
		scanner := bufio.NewScanner(outPR)
		scanner.Buffer(make([]byte, 1*1024*1024), 1*1024*1024)
		scanner.Split(splitJSONLines)
		var resp map[string]any
		if scanner.Scan() {
			json.Unmarshal(scanner.Bytes(), &resp) //nolint:errcheck
		}
		return resp
	}
	nextID := func() string {
		msgID++
		return fmt.Sprintf("%d", msgID)
	}

	// warmup both roots
	r1 := send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"warmup","arguments":{"root":"/tmp/backend"}}}`, nextID()))
	if r1["error"] != nil {
		t.Fatalf("backend warmup error: %v", r1["error"])
	}
	r2 := send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"warmup","arguments":{"root":"/tmp/frontend"}}}`, nextID()))
	if r2["error"] != nil {
		t.Fatalf("frontend warmup error: %v", r2["error"])
	}

	// both roots should still be warming up
	findBackend := send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/tmp/backend","query":""}}}`, nextID()))
	if findBackend["error"] == nil {
		t.Fatalf("expected retry error for backend while warming up, got: %v", findBackend)
	}

	// release backend only
	close(backendRelease)

	// poll until backend is ready
	deadline := time.Now().Add(3 * time.Second)
	var backendReady map[string]any
	for time.Now().Before(deadline) {
		backendReady = send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/tmp/backend","query":""}}}`, nextID()))
		if backendReady["error"] == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if backendReady["error"] != nil {
		t.Fatalf("backend find_symbols still failing: %v", backendReady["error"])
	}
	backendText := backendReady["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(backendText, "GoHandler") {
		t.Errorf("expected GoHandler in backend result, got: %s", backendText)
	}

	// frontend should still be warming up (independent of backend)
	frontendResp := send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/tmp/frontend","query":""}}}`, nextID()))
	if frontendResp["error"] == nil {
		t.Fatalf("expected frontend to still be warming up, got result: %v", frontendResp)
	}

	// release frontend
	close(frontendRelease)

	// poll until frontend is ready
	deadline = time.Now().Add(3 * time.Second)
	var frontendReady map[string]any
	for time.Now().Before(deadline) {
		frontendReady = send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/tmp/frontend","query":""}}}`, nextID()))
		if frontendReady["error"] == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if frontendReady["error"] != nil {
		t.Fatalf("frontend find_symbols still failing: %v", frontendReady["error"])
	}
	frontendText := frontendReady["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(frontendText, "TsComponent") {
		t.Errorf("expected TsComponent in frontend result, got: %s", frontendText)
	}
	if strings.Contains(frontendText, "GoHandler") {
		t.Errorf("GoHandler should not appear in frontend result, got: %s", frontendText)
	}
}

// Type definitions used only at test time to validate the shape of tools.json.
// Production code embeds tools.json as json.RawMessage and serves it verbatim.
type toolProperty struct {
	Type        string        `json:"type"`
	Description string        `json:"description,omitempty"`
	Items       *toolProperty `json:"items,omitempty"`
}

type toolInputSchema struct {
	Type       string                  `json:"type"`
	Properties map[string]toolProperty `json:"properties"`
	Required   []string                `json:"required"`
}

type toolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema toolInputSchema `json:"inputSchema"`
}

func TestToolsJSON(t *testing.T) {
	var defs []toolDefinition
	dec := json.NewDecoder(bytes.NewReader(toolsJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&defs); err != nil {
		t.Fatalf("tools.json failed strict decode: %v", err)
	}
	if len(defs) != 3 {
		t.Fatalf("expected 3 tool definitions, got %d", len(defs))
	}
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
		if d.InputSchema.Type != "object" {
			t.Errorf("tool %q: inputSchema.type = %q, want %q", d.Name, d.InputSchema.Type, "object")
		}
	}
	for _, want := range []string{"warmup", "find_references", "find_symbols"} {
		if !names[want] {
			t.Errorf("tool %q not found in tools.json", want)
		}
	}
}

func TestServe_UnknownMethod(t *testing.T) {
	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a := newWithIO("/", domain.Graph{}, &stubEditor{}, inPR, outPW)

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
