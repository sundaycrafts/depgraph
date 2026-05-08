package mcp

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// newTestSession builds a LiveSession suitable for unit tests of the
// document-sync helpers (DidOpen / DidChange / DidClose / runEventLoop).
// It wires conn at a bytes.Buffer so callers can inspect the raw LSP
// notifications that were emitted.
func newTestSession(t *testing.T, lang lsploader.Language, root string) (*LiveSession, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	// readLoop is not started; notifications never expect responses so
	// this is fine for these tests.
	conn := newLSPConn(strings.NewReader(""), out, slog.Default())
	return &LiveSession{
		Lang:       lang,
		Root:       root,
		conn:       conn,
		logger:     slog.Default(),
		openedURIs: make(map[string]bool),
		versions:   make(map[string]int64),
	}, out
}

// expectNotification scans buf for an LSP notification with the given
// method. It returns the JSON body as a string for further assertions.
// The buffer holds Content-Length-framed messages; we extract bodies by
// splitting on the blank line that follows the headers.
func expectNotification(t *testing.T, buf *bytes.Buffer, method string) string {
	t.Helper()
	raw := buf.String()
	bodies := extractBodies(t, raw)
	for _, body := range bodies {
		if strings.Contains(body, `"method":"`+method+`"`) {
			return body
		}
	}
	t.Fatalf("expected %s notification, got: %s", method, raw)
	return ""
}

func expectNoNotification(t *testing.T, buf *bytes.Buffer, method string) {
	t.Helper()
	if strings.Contains(buf.String(), `"method":"`+method+`"`) {
		t.Fatalf("did not expect %s notification, got: %s", method, buf.String())
	}
}

// extractBodies splits Content-Length-framed messages and returns each body.
func extractBodies(t *testing.T, raw string) []string {
	t.Helper()
	var bodies []string
	for {
		idx := strings.Index(raw, "\r\n\r\n")
		if idx == -1 {
			break
		}
		header := raw[:idx]
		body := raw[idx+4:]
		// crude parse of Content-Length
		var n int
		for _, line := range strings.Split(header, "\r\n") {
			if strings.HasPrefix(line, "Content-Length:") {
				_, err := fmtSscanf(strings.TrimPrefix(line, "Content-Length:"), &n)
				if err != nil {
					t.Fatalf("parse content-length: %v", err)
				}
			}
		}
		if len(body) < n {
			break
		}
		bodies = append(bodies, body[:n])
		raw = body[n:]
	}
	return bodies
}

// fmtSscanf is a tiny shim around fmt.Sscanf so the import block in this
// file stays compact. (Avoids importing fmt for one call.)
func fmtSscanf(s string, target *int) (int, error) {
	s = strings.TrimSpace(s)
	v := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, &parseErr{s: s}
		}
		v = v*10 + int(r-'0')
	}
	*target = v
	return 1, nil
}

type parseErr struct{ s string }

func (e *parseErr) Error() string { return "parse: " + e.s }

func TestLiveSession_DidOpen_TracksVersionAndOpenSet(t *testing.T) {
	s, buf := newTestSession(t, lsploader.TypeScript, "/tmp/x")
	if err := s.DidOpen("file:///tmp/x/a.ts", "typescript", "alpha"); err != nil {
		t.Fatal(err)
	}
	if !s.openedURIs["file:///tmp/x/a.ts"] {
		t.Error("expected URI to be in open set")
	}
	if s.versions["file:///tmp/x/a.ts"] != 1 {
		t.Errorf("version = %d, want 1", s.versions["file:///tmp/x/a.ts"])
	}
	body := expectNotification(t, buf, "textDocument/didOpen")
	if !strings.Contains(body, `"version":1`) {
		t.Errorf("expected version 1 in didOpen, got: %s", body)
	}
}

func TestLiveSession_DidChange_IncrementsVersion(t *testing.T) {
	s, buf := newTestSession(t, lsploader.TypeScript, "/tmp/x")
	uri := "file:///tmp/x/a.ts"
	_ = s.DidOpen(uri, "typescript", "v1")

	if err := s.DidChange(uri, "v2"); err != nil {
		t.Fatal(err)
	}
	if s.versions[uri] != 2 {
		t.Errorf("version after one didChange = %d, want 2", s.versions[uri])
	}
	if err := s.DidChange(uri, "v3"); err != nil {
		t.Fatal(err)
	}
	if s.versions[uri] != 3 {
		t.Errorf("version after two didChanges = %d, want 3", s.versions[uri])
	}

	// Inspect the second didChange explicitly to confirm version=3 wire.
	bodies := extractBodies(t, buf.String())
	var changeBodies []string
	for _, b := range bodies {
		if strings.Contains(b, `"method":"textDocument/didChange"`) {
			changeBodies = append(changeBodies, b)
		}
	}
	if len(changeBodies) != 2 {
		t.Fatalf("expected 2 didChange notifications, got %d", len(changeBodies))
	}
	if !strings.Contains(changeBodies[0], `"version":2`) {
		t.Errorf("first didChange should carry version 2: %s", changeBodies[0])
	}
	if !strings.Contains(changeBodies[1], `"version":3`) {
		t.Errorf("second didChange should carry version 3: %s", changeBodies[1])
	}
}

func TestLiveSession_DidClose_ClearsTracking(t *testing.T) {
	s, _ := newTestSession(t, lsploader.TypeScript, "/tmp/x")
	uri := "file:///tmp/x/a.ts"
	_ = s.DidOpen(uri, "typescript", "x")
	_ = s.DidClose(uri)
	if s.openedURIs[uri] {
		t.Error("expected URI to be removed from open set")
	}
	if _, ok := s.versions[uri]; ok {
		t.Error("expected version to be removed")
	}
}

func TestLiveSession_RunEventLoop_CreateModifyDelete(t *testing.T) {
	root := t.TempDir()
	s, buf := newTestSession(t, lsploader.TypeScript, root)
	events := make(chan FileEvent, 8)
	done := make(chan struct{})
	go func() {
		s.runEventLoop(events, nil)
		close(done)
	}()

	file := filepath.Join(root, "a.ts")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	events <- FileEvent{Path: file, Op: FileCreated}
	waitFor(t, func() bool {
		return strings.Contains(buf.String(), `"method":"textDocument/didOpen"`)
	}, "didOpen after Create")

	if err := os.WriteFile(file, []byte("hello-2"), 0o644); err != nil {
		t.Fatal(err)
	}
	events <- FileEvent{Path: file, Op: FileModified}
	waitFor(t, func() bool {
		return strings.Contains(buf.String(), `"method":"textDocument/didChange"`)
	}, "didChange after Modify")

	events <- FileEvent{Path: file, Op: FileDeleted}
	waitFor(t, func() bool {
		return strings.Contains(buf.String(), `"method":"textDocument/didClose"`)
	}, "didClose after Delete")

	close(events)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event loop did not exit after channel close")
	}
}

func TestLiveSession_RunEventLoop_FiltersByExtension(t *testing.T) {
	root := t.TempDir()
	s, buf := newTestSession(t, lsploader.TypeScript, root)
	events := make(chan FileEvent, 4)
	go s.runEventLoop(events, nil)

	noise := filepath.Join(root, "README.md")
	if err := os.WriteFile(noise, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	events <- FileEvent{Path: noise, Op: FileCreated}

	// Give the loop time to process; verify nothing was sent.
	time.Sleep(100 * time.Millisecond)
	expectNoNotification(t, buf, "textDocument/didOpen")
	close(events)
}

func TestLiveSession_RunEventLoop_FiltersByExcludes(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "vendor"))
	noise := filepath.Join(root, "vendor", "x.ts")
	if err := os.WriteFile(noise, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, buf := newTestSession(t, lsploader.TypeScript, root)
	events := make(chan FileEvent, 4)
	go s.runEventLoop(events, []string{"vendor/**"})

	events <- FileEvent{Path: noise, Op: FileCreated}
	time.Sleep(100 * time.Millisecond)
	expectNoNotification(t, buf, "textDocument/didOpen")
	close(events)
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
