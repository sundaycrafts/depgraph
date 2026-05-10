package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sundaycrafts/depgraph/internal/config"
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
	return New(ws, NewNoopInitializer(), nil, nil, "", slog.Default())
}

// recordingWatcher is a no-op SourceWatcher used so AddProject succeeds
// in tests. It never emits events; sessions stay in IndexIndexing.
type recordingWatcher struct{}

func (recordingWatcher) Subscribe() <-chan domain.SourceChange {
	ch := make(chan domain.SourceChange)
	close(ch)
	return ch
}
func (recordingWatcher) Stop() {}

// recordingWatcherFactory satisfies PortSourceWatcherFactory and remembers
// every Watch() call so tests can assert which roots got registered.
type recordingWatcherFactory struct {
	roots []string
}

func (r *recordingWatcherFactory) Watch(root string, _ []string) (domain.PortSourceWatcher, error) {
	r.roots = append(r.roots, root)
	return recordingWatcher{}, nil
}

// fakeDetectorGo always reports Go so AddProject can proceed past the
// language-detection stage in tests where the test root is just an
// empty temp directory.
type fakeDetectorGo struct{}

func (fakeDetectorGo) Detect(_ string) ([]lsploader.Language, error) {
	return []lsploader.Language{lsploader.Go}, nil
}

// adapterWithRecordingWS builds an Adapter with a workspace whose watcher
// factory records every AddProject call. Returns the adapter plus a
// pointer to the slice of registered roots so tests can assert.
func adapterWithRecordingWS(t *testing.T, cfg *config.Config, warnings []string, configCWD string) (*Adapter, *[]string) {
	t.Helper()
	wf := &recordingWatcherFactory{}
	ws := domain.NewWorkspace(
		context.Background(),
		nopSessionFactory{},
		wf,
		fakeDetectorGo{},
		fakeLocator{available: map[string]string{"gopls": "/bin/true"}},
		slog.Default(),
	)
	a := New(ws, NewNoopInitializer(), cfg, warnings, configCWD, slog.Default())
	return a, &wf.roots
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

// dispatchRequest invokes a.dispatch directly so multiple calls share
// one workspace across the test (Serve's deferred Shutdown would clear
// projects between invocations).
func dispatchRequest(t *testing.T, a *Adapter, body string) (mcpResult, *rpcErr) {
	t.Helper()
	var msg rpcMsg
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return a.dispatch(context.Background(), msg)
}

// TestServe_Initialize_RegistersConfigProjects asserts the config-driven
// startup path: each entry in startupCfg.Projects is registered via
// AddProject (observed by recordingWatcherFactory) and the legacy CWD
// auto-add path is skipped.
func TestServe_Initialize_RegistersConfigProjects(t *testing.T) {
	tmp := t.TempDir()
	rootA := filepath.Join(tmp, "a")
	rootB := filepath.Join(tmp, "b")
	for _, r := range []string{rootA, rootB} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Projects: []config.Project{
			{Root: rootA, Excludes: []string{"**/*.test.ts"}, ExcludeSymbols: []string{"getServerSideProps"}},
			{Root: rootB},
		},
	}

	a, roots := adapterWithRecordingWS(t, cfg, nil, tmp)
	res, rerr := dispatchRequest(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
	)
	if rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}
	ir, ok := res.(initializeResult)
	if !ok {
		t.Fatalf("expected initializeResult, got %T", res)
	}
	if len(ir.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", ir.Warnings)
	}
	if len(*roots) != 2 {
		t.Fatalf("expected 2 AddProject calls, got %d: %v", len(*roots), *roots)
	}
	if (*roots)[0] != rootA || (*roots)[1] != rootB {
		t.Errorf("unexpected registered roots: %v", *roots)
	}

	// initialize.projects must list every registered root with its
	// detected language so the agent has a discovery path that does
	// not require add_project or filesystem reads.
	if len(ir.Projects) != 2 {
		t.Fatalf("expected 2 projects in initialize response, got %d: %+v", len(ir.Projects), ir.Projects)
	}
	if ir.Projects[0].Root != rootA || ir.Projects[1].Root != rootB {
		t.Errorf("initialize.projects roots = %v, want [%s, %s]", ir.Projects, rootA, rootB)
	}
	for _, p := range ir.Projects {
		if len(p.Languages) == 0 {
			t.Errorf("initialize.projects[%q].Languages is empty", p.Root)
		}
	}
}

// TestServe_LookupProject_ErrorListsRegisteredRoots asserts that when
// the agent passes a wrong root (e.g. the parent directory), the error
// surfaces every currently-registered root so the agent self-corrects
// without an extra round-trip.
func TestServe_LookupProject_ErrorListsRegisteredRoots(t *testing.T) {
	tmp := t.TempDir()
	rootA := filepath.Join(tmp, "frontend")
	if err := os.MkdirAll(rootA, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Projects: []config.Project{{Root: rootA}}}
	a, _ := adapterWithRecordingWS(t, cfg, nil, tmp)
	if _, rerr := dispatchRequest(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
	); rerr != nil {
		t.Fatalf("initialize: %v", rerr)
	}

	// Probe the parent path — must error with a message that includes
	// the actually-registered subroot.
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"find_symbols","arguments":{"root":"` + tmp + `","query":"x"}}}`
	_, rerr := dispatchRequest(t, a, body)
	if rerr == nil {
		t.Fatal("expected error for parent root, got success")
	}
	if !strings.Contains(rerr.Message, "registered") {
		t.Errorf("error message missing 'registered' hint: %q", rerr.Message)
	}
	if !strings.Contains(rerr.Message, rootA) {
		t.Errorf("error message missing registered root %q: %q", rootA, rerr.Message)
	}
}

// TestServe_Initialize_SurfacesStartupWarnings asserts pre-loaded
// warnings (from config.Load) are surfaced on the initialize response.
func TestServe_Initialize_SurfacesStartupWarnings(t *testing.T) {
	cfg := &config.Config{} // no projects but file existed
	startupWarn := []string{"depgraph.yaml: projects[0]: root \"\" is empty, skipping entry"}
	a, _ := adapterWithRecordingWS(t, cfg, startupWarn, t.TempDir())
	res, rerr := dispatchRequest(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
	)
	if rerr != nil {
		t.Fatalf("unexpected error: %v", rerr)
	}
	ir := res.(initializeResult)
	if len(ir.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(ir.Warnings), ir.Warnings)
	}
	if !strings.Contains(ir.Warnings[0], "depgraph.yaml") {
		t.Errorf("warning %q missing depgraph.yaml prefix", ir.Warnings[0])
	}
}

// TestServe_ReloadConfig asserts the tool re-reads the config file on
// disk and registers any newly listed projects.
func TestServe_ReloadConfig(t *testing.T) {
	tmp := t.TempDir()
	rootInit := filepath.Join(tmp, "init")
	rootAdded := filepath.Join(tmp, "added")
	for _, r := range []string{rootInit, rootAdded} {
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Initial in-memory config has only rootInit; the on-disk YAML has
	// both, so reload_config should pick up rootAdded.
	initialCfg := &config.Config{Projects: []config.Project{{Root: rootInit}}}
	if err := os.WriteFile(filepath.Join(tmp, "depgraph.yaml"),
		[]byte("projects:\n  - root: "+rootInit+"\n  - root: "+rootAdded+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, roots := adapterWithRecordingWS(t, initialCfg, nil, tmp)
	if _, rerr := dispatchRequest(t, a,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
	); rerr != nil {
		t.Fatalf("initialize error: %v", rerr)
	}
	if len(*roots) != 1 || (*roots)[0] != rootInit {
		t.Fatalf("after initialize, expected [%s], got %v", rootInit, *roots)
	}

	res, rerr := dispatchRequest(t, a,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"reload_config","arguments":{}}}`,
	)
	if rerr != nil {
		t.Fatalf("reload_config error: %v", rerr)
	}
	// Workspace.AddProject is idempotent: calling it again for rootInit
	// returns the existing project without invoking the watcher factory,
	// so the recorded list should be exactly [init, added].
	if len(*roots) != 2 || (*roots)[1] != rootAdded {
		t.Errorf("after reload, expected roots [%s, %s], got %v", rootInit, rootAdded, *roots)
	}
	tcr, ok := res.(toolCallResult)
	if !ok {
		t.Fatalf("expected toolCallResult, got %T", res)
	}
	if len(tcr.Content) != 1 {
		t.Fatalf("expected 1 content block, got %v", tcr.Content)
	}
	var payload struct {
		Status   string        `json:"status"`
		Warnings []string      `json:"warnings"`
		Projects []projectInfo `json:"projects"`
	}
	if err := json.Unmarshal([]byte(tcr.Content[0].Text), &payload); err != nil {
		t.Fatalf("decode reload_config payload: %v", err)
	}
	if payload.Status != "reloaded" {
		t.Errorf("status = %q, want \"reloaded\"", payload.Status)
	}
	// reload_config must echo the post-apply project list so the agent
	// learns about newly added roots without an extra round-trip.
	if len(payload.Projects) != 2 {
		t.Fatalf("expected 2 projects in reload_config payload, got %d: %+v", len(payload.Projects), payload.Projects)
	}
	if payload.Projects[0].Root != rootAdded || payload.Projects[1].Root != rootInit {
		// Sorted by Root, so "added" lexicographically precedes "init".
		t.Errorf("reload_config.projects roots = %v, want [%s, %s]", payload.Projects, rootAdded, rootInit)
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
	if len(tools) != 4 {
		t.Errorf("expected 4 tools, got %d", len(tools))
	}
	wantNames := map[string]bool{
		"add_project":     false,
		"find_symbols":    false,
		"find_references": false,
		"reload_config":   false,
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
	if len(defs) != 4 {
		t.Fatalf("expected 4 tool definitions, got %d", len(defs))
	}
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
		if d.InputSchema.Type != "object" {
			t.Errorf("tool %q: inputSchema.type = %q, want %q", d.Name, d.InputSchema.Type, "object")
		}
	}
	for _, want := range []string{"add_project", "find_references", "find_symbols", "reload_config"} {
		if !names[want] {
			t.Errorf("tool %q not found in tools.json", want)
		}
	}
}
