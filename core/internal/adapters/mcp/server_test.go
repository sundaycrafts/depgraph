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

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// frame wraps a JSON body in newline-delimited format (MCP 2025-11-25 stdio transport).
func frame(body string) string {
	return body + "\n"
}

// fakeLocator implements lsploader.Locator. By default it claims every
// binary is missing — sufficient for tests that never call add_component.
type fakeLocator struct {
	available map[string]string
}

func (f fakeLocator) LookupBinary(name string) (string, error) {
	if path, ok := f.available[name]; ok {
		return path, nil
	}
	return "", fmt.Errorf("not found: %s", name)
}

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	cfg, err := LoadEmbeddedConfig()
	if err != nil {
		t.Fatalf("load embedded config: %v", err)
	}
	mgr := NewComponentManager(context.Background(), cfg, fakeLocator{}, slog.Default())
	return New(mgr, NewNoopInitializer(), slog.Default())
}

// serveOne runs the adapter, sends one request, reads one response, then cancels.
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
		"add_component":   false,
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

func TestServe_AddComponent_MissingRoot(t *testing.T) {
	a := newTestAdapter(t)
	resp := serveOne(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_component","arguments":{}}}`,
	)
	if _, ok := resp["error"].(map[string]any); !ok {
		t.Fatalf("expected error for missing root, got: %v", resp)
	}
}

func TestServe_FindSymbols_ComponentNotRegistered(t *testing.T) {
	a := newTestAdapter(t)
	resp := serveOne(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"/tmp/missing","query":"foo"}}}`,
	)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got: %v", resp)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "add_component") {
		t.Errorf("expected message to mention add_component, got: %s", msg)
	}
}

func TestServe_FindReferences_DecodesSymbolID(t *testing.T) {
	a := newTestAdapter(t)
	// SymbolID is well-formed but the component is not registered, so we
	// expect the "call add_component first" error path — proving the symbol
	// ID is decoded successfully.
	id := EncodeSymbolID(SymbolID{Lang: lsploader.Go, RelPath: "x.go", Line: 1, Char: 2, Name: "Foo"})
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_references","arguments":{"root":"/tmp/missing","symbol_id":%q}}}`, id)
	resp := serveOne(t, a, body)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got: %v", resp)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "add_component") {
		t.Errorf("expected component-missing error, got: %s", msg)
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
	for _, want := range []string{"add_component", "find_references", "find_symbols"} {
		if !names[want] {
			t.Errorf("tool %q not found in tools.json", want)
		}
	}
}

func TestSymbolID_RoundTrip(t *testing.T) {
	cases := []SymbolID{
		{Lang: lsploader.Go, RelPath: "internal/foo.go", Line: 12, Char: 5, Name: "Foo"},
		{Lang: lsploader.Rust, RelPath: "src/lib.rs", Line: 0, Char: 0, Name: "Trait::method"},
		{Lang: lsploader.TypeScript, RelPath: "src/x:y.ts", Line: 99, Char: 200, Name: "name with space"},
	}
	for _, want := range cases {
		got, err := DecodeSymbolID(EncodeSymbolID(want))
		if err != nil {
			t.Fatalf("decode failed for %#v: %v", want, err)
		}
		if got != want {
			t.Errorf("round-trip mismatch:\n  got  %#v\n  want %#v", got, want)
		}
	}
}

func TestSymbolID_DecodeRejectsMalformed(t *testing.T) {
	bad := []string{"", "only-three:parts:1", "go:foo.go:not-a-line:0:Zm9v"}
	for _, s := range bad {
		if _, err := DecodeSymbolID(s); err == nil {
			t.Errorf("expected error decoding %q, got nil", s)
		}
	}
}

func TestEmbeddedConfig_AllLanguagesPresent(t *testing.T) {
	cfg, err := LoadEmbeddedConfig()
	if err != nil {
		t.Fatalf("LoadEmbeddedConfig: %v", err)
	}
	for _, lang := range lsploader.All() {
		specs, err := cfg.LSPSpecsFor(lang)
		if err != nil {
			t.Errorf("LSPSpecsFor(%s): %v", lang, err)
			continue
		}
		if len(specs) == 0 {
			t.Errorf("language %s has no LSP servers configured", lang)
		}
		for _, spec := range specs {
			if spec.Command == "" {
				t.Errorf("language %s: LSP spec has empty Command: %#v", lang, spec)
			}
		}
	}
}

func TestComponentState_InitiallyIndexing(t *testing.T) {
	c := &Component{Root: "/tmp", sessions: make(map[lsploader.Language]*sessionEntry)}
	c.sessions[lsploader.Go] = &sessionEntry{state: ComponentIndexing}
	state, _ := c.State()
	if state != ComponentIndexing {
		t.Errorf("state=%s want indexing", state)
	}
}

func TestComponentState_AllReady(t *testing.T) {
	c := &Component{Root: "/tmp", sessions: make(map[lsploader.Language]*sessionEntry)}
	c.sessions[lsploader.Go] = &sessionEntry{state: ComponentReady}
	c.sessions[lsploader.TypeScript] = &sessionEntry{state: ComponentReady}
	state, _ := c.State()
	if state != ComponentReady {
		t.Errorf("state=%s want ready", state)
	}
}

func TestComponentState_OneFailedFailsAggregate(t *testing.T) {
	c := &Component{Root: "/tmp", sessions: make(map[lsploader.Language]*sessionEntry)}
	c.sessions[lsploader.Go] = &sessionEntry{state: ComponentReady}
	c.sessions[lsploader.TypeScript] = &sessionEntry{state: ComponentFailed, err: fmt.Errorf("boom")}
	state, err := c.State()
	if state != ComponentFailed {
		t.Errorf("state=%s want failed", state)
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err=%v want to contain 'boom'", err)
	}
}

func TestRangeContains(t *testing.T) {
	r := LSPRange{
		Start: LSPPosition{Line: 1, Character: 2},
		End:   LSPPosition{Line: 3, Character: 4},
	}
	cases := []struct {
		pos  LSPPosition
		want bool
	}{
		{LSPPosition{Line: 1, Character: 2}, true},
		{LSPPosition{Line: 1, Character: 1}, false},
		{LSPPosition{Line: 2, Character: 0}, true},
		{LSPPosition{Line: 3, Character: 3}, true},
		{LSPPosition{Line: 3, Character: 4}, false}, // exclusive end
		{LSPPosition{Line: 4, Character: 0}, false},
	}
	for _, c := range cases {
		if got := rangeContains(r, c.pos); got != c.want {
			t.Errorf("rangeContains(%v) = %v want %v", c.pos, got, c.want)
		}
	}
}

func TestFindInnermostSymbol(t *testing.T) {
	outer := LSPDocumentSymbol{
		Name:           "Outer",
		Range:          LSPRange{Start: LSPPosition{Line: 0}, End: LSPPosition{Line: 100}},
		SelectionRange: LSPRange{Start: LSPPosition{Line: 0, Character: 5}},
		Children: []LSPDocumentSymbol{
			{
				Name:           "Inner",
				Range:          LSPRange{Start: LSPPosition{Line: 10}, End: LSPPosition{Line: 20}},
				SelectionRange: LSPRange{Start: LSPPosition{Line: 10, Character: 2}},
			},
		},
	}
	got := findInnermostSymbol([]LSPDocumentSymbol{outer}, LSPPosition{Line: 15})
	if got == nil || got.Name != "Inner" {
		t.Errorf("expected Inner, got %v", got)
	}
	got = findInnermostSymbol([]LSPDocumentSymbol{outer}, LSPPosition{Line: 50})
	if got == nil || got.Name != "Outer" {
		t.Errorf("expected Outer, got %v", got)
	}
	got = findInnermostSymbol([]LSPDocumentSymbol{outer}, LSPPosition{Line: 200})
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestIsExcluded(t *testing.T) {
	excludes := []string{"**/*_test.go", "vendor/**"}
	cases := []struct {
		path string
		want bool
	}{
		{"foo_test.go", true},
		{"a/b/foo_test.go", true},
		{"vendor/x.go", true},
		{"foo.go", false},
		{"src/foo.ts", false},
	}
	for _, c := range cases {
		got, err := isExcluded(c.path, excludes)
		if err != nil {
			t.Errorf("isExcluded(%q) returned err: %v", c.path, err)
		}
		if got != c.want {
			t.Errorf("isExcluded(%q) = %v want %v", c.path, got, c.want)
		}
	}
}

func TestIsExcluded_InvalidPattern(t *testing.T) {
	// doublestar.PathMatch returns ErrBadPattern for unbalanced brackets.
	_, err := isExcluded("foo.go", []string{"foo[unbalanced"})
	if err == nil {
		t.Error("expected error for invalid glob pattern, got nil")
	}
}
