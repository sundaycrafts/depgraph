package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// frame wraps a JSON body in newline-delimited format (MCP 2025-11-25
// stdio transport).
func frame(body string) string {
	return body + "\n"
}

// fakeLocator implements lsploader.Locator. By default it claims every
// binary is missing — sufficient for tests that never call add_project.
type fakeLocator struct {
	available map[string]string
}

func (f fakeLocator) LookupBinary(name string) (string, error) {
	if path, ok := f.available[name]; ok {
		return path, nil
	}
	return "", fmt.Errorf("not found: %s", name)
}

// nopSessionFactory satisfies domain.PortAnalysisSessionFactory but never
// actually opens anything — Open is unreachable in dispatch tests because
// no test in this file calls add_project for a directory with marker files.
type nopSessionFactory struct{}

func (nopSessionFactory) Open(_ context.Context, _ lsploader.Language, _ string, _ []string, _ <-chan domain.SourceChange) (domain.PortAnalysisSession, error) {
	return nil, fmt.Errorf("nopSessionFactory: Open not implemented")
}

// nopWatcherFactory satisfies domain.PortSourceWatcherFactory.
type nopWatcherFactory struct{}

func (nopWatcherFactory) Watch(_ string, _ []string) (domain.PortSourceWatcher, error) {
	return nil, fmt.Errorf("nopWatcherFactory: Watch not implemented")
}

// nopDetector satisfies domain.PortLanguageDetector.
type nopDetector struct{}

func (nopDetector) Detect(_ string) ([]lsploader.Language, error) { return nil, nil }

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	ws := domain.NewWorkspace(
		context.Background(),
		nopSessionFactory{},
		nopWatcherFactory{},
		nopDetector{},
		fakeLocator{},
		slog.Default(),
	)
	return New(ws, NewNoopInitializer(), slog.Default())
}

// serveOne runs the adapter, sends one request, reads one response, then
// cancels.
func serveOne(t *testing.T, a *Adapter, reqBody string) map[string]any {
	t.Helper()
	inPR, inPW := io.Pipe()
	outPR, outPW := io.Pipe()
	a.in = inPR
	a.out = outPW

	ctx, cancel := context.WithCancel(context.Background())

	scanner := bufio.NewScanner(outPR)
	scanner.Buffer(make([]byte, 1*1024*1024), 1*1024*1024)
	scanner.Split(splitJSONLines)

	done := make(chan struct{})
	var resp map[string]any
	go func() {
		defer close(done)
		if scanner.Scan() {
			_ = json.Unmarshal(scanner.Bytes(), &resp)
		}
	}()

	go a.Serve(ctx) //nolint:errcheck

	fmt.Fprint(inPW, frame(reqBody))

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
	body := `{"jsonrpc":"2.0"}`
	adv, tok, err := splitJSONLines([]byte(body), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adv != 0 || tok != nil {
		t.Errorf("expected no progress, got advance=%d token=%q", adv, tok)
	}
}

func TestServe_Initialize(t *testing.T) {
	a := newTestAdapter(t)
	resp := serveOne(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`,
	)
	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("unexpected protocolVersion: %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "depgraph" {
		t.Errorf("serverInfo.name=%v want depgraph", info["name"])
	}
	if instr, _ := result["instructions"].(string); instr == "" {
		t.Errorf("expected non-empty instructions")
	}
}

func TestServe_ToolsList(t *testing.T) {
	a := newTestAdapter(t)
	resp := serveOne(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
	)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(tools))
	}
	wantNames := map[string]bool{
		"add_project":     false,
		"find_symbols":    false,
		"find_references": false,
	}
	for _, tool := range tools {
		m, _ := tool.(map[string]any)
		name, _ := m["name"].(string)
		if _, ok := wantNames[name]; ok {
			wantNames[name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("tool %q missing from tools/list", name)
		}
	}
}

func TestServe_UnknownMethod(t *testing.T) {
	a := newTestAdapter(t)
	resp := serveOne(t, a,
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

func TestServe_AddProject_MissingRoot(t *testing.T) {
	a := newTestAdapter(t)
	resp := serveOne(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_project","arguments":{}}}`,
	)
	if _, ok := resp["error"].(map[string]any); !ok {
		t.Fatalf("expected error for missing root, got: %v", resp)
	}
}

func TestServe_FindSymbols_ProjectNotRegistered(t *testing.T) {
	a := newTestAdapter(t)
	resp := serveOne(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/tmp/missing","query":"foo"}}}`,
	)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got: %v", resp)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "add_project") {
		t.Errorf("expected message to mention add_project, got: %s", msg)
	}
}

func TestServe_FindReferences_DecodesSymbolID(t *testing.T) {
	a := newTestAdapter(t)
	// SymbolID is well-formed but the project is not registered, so we
	// expect the "call add_project first" error path — proving the
	// symbol ID is decoded successfully.
	id := domain.EncodeSymbolID(domain.SymbolID{Lang: lsploader.Go, RelPath: "x.go", Line: 1, Char: 2, Name: "Foo"})
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_references","arguments":{"root":"/tmp/missing","symbol_id":%q}}}`, id)
	resp := serveOne(t, a, body)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got: %v", resp)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "add_project") {
		t.Errorf("expected project-missing error, got: %s", msg)
	}
}

func TestServe_FindReferences_InvalidSymbolID(t *testing.T) {
	a := newTestAdapter(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_references","arguments":{"root":"/tmp","symbol_id":"not-valid"}}}`
	resp := serveOne(t, a, body)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got: %v", resp)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "symbol_id") && !strings.Contains(msg, "invalid") {
		t.Errorf("expected validation error, got: %s", msg)
	}
}

// Type definitions used only at test time to validate the shape of tools.json.
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
	for _, want := range []string{"add_project", "find_references", "find_symbols"} {
		if !names[want] {
			t.Errorf("tool %q not found in tools.json", want)
		}
	}
}
