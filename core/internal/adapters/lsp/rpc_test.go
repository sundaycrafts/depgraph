package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// writerFunc is an io.Writer backed by a function, used to intercept writes in tests.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// pipeConn wires up a conn with an io.Pipe as the response reader.
// The caller feeds responses via the returned *io.PipeWriter.
// Responses are only written after the first request is sent, ensuring
// readLoop cannot process a response before call registers its pending channel.
func pipeConn(w io.Writer) (*conn, *io.PipeWriter, chan struct{}) {
	pr, pw := io.Pipe()
	sent := make(chan struct{})
	var once sync.Once
	c := newConn(pr, writerFunc(func(p []byte) (int, error) {
		n, err := w.Write(p)
		once.Do(func() { close(sent) })
		return n, err
	}), slog.New(slog.DiscardHandler))
	return c, pw, sent
}

func TestReadMessage_Single(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{}}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	c := newConn(strings.NewReader(frame), io.Discard, slog.New(slog.DiscardHandler))

	got, err := c.readMessage()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != body {
		t.Errorf("body=%q, want %q", got, body)
	}
}

func TestReadMessage_TwoBackToBack(t *testing.T) {
	a := `{"jsonrpc":"2.0","id":1,"result":1}`
	b := `{"jsonrpc":"2.0","id":2,"result":2}`
	stream := fmt.Sprintf("Content-Length: %d\r\n\r\n%sContent-Length: %d\r\n\r\n%s", len(a), a, len(b), b)
	c := newConn(strings.NewReader(stream), io.Discard, slog.New(slog.DiscardHandler))

	for i, want := range []string{a, b} {
		got, err := c.readMessage()
		if err != nil {
			t.Fatalf("message %d: unexpected error: %v", i, err)
		}
		if string(got) != want {
			t.Errorf("message %d: body=%q, want %q", i, got, want)
		}
	}
}

func TestReadMessage_LargeBody(t *testing.T) {
	// 4MB+1 body — would have triggered bufio.ErrTooLong under the old
	// scanner-based implementation with its 4MB cap.
	const size = 4*1024*1024 + 1
	big := strings.Repeat("x", size)
	body := `{"data":"` + big + `"}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	c := newConn(strings.NewReader(frame), io.Discard, slog.New(slog.DiscardHandler))

	got, err := c.readMessage()
	if err != nil {
		t.Fatalf("unexpected error reading large body: %v", err)
	}
	if len(got) != len(body) {
		t.Errorf("body length=%d, want %d", len(got), len(body))
	}
}

func TestReadMessage_EOFAtMessageBoundary(t *testing.T) {
	c := newConn(strings.NewReader(""), io.Discard, slog.New(slog.DiscardHandler))
	_, err := c.readMessage()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got: %v", err)
	}
}

func TestReadMessage_EOFInBody(t *testing.T) {
	// Header announces 100 bytes but stream provides only 5.
	frame := "Content-Length: 100\r\n\r\nshort"
	c := newConn(strings.NewReader(frame), io.Discard, slog.New(slog.DiscardHandler))
	_, err := c.readMessage()
	if err == nil {
		t.Fatal("expected error reading truncated body, got nil")
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("expected ErrUnexpectedEOF (or similar), got plain EOF: %v", err)
	}
}

func TestReadMessage_MissingContentLength(t *testing.T) {
	frame := "Some-Other-Header: x\r\n\r\nhello"
	c := newConn(strings.NewReader(frame), io.Discard, slog.New(slog.DiscardHandler))
	_, err := c.readMessage()
	if err == nil || !strings.Contains(err.Error(), "missing Content-Length") {
		t.Errorf("expected missing Content-Length error, got: %v", err)
	}
}

func TestReadMessage_InvalidContentLength(t *testing.T) {
	frame := "Content-Length: not-a-number\r\n\r\nx"
	c := newConn(strings.NewReader(frame), io.Discard, slog.New(slog.DiscardHandler))
	_, err := c.readMessage()
	if err == nil || !strings.Contains(err.Error(), "invalid Content-Length") {
		t.Errorf("expected invalid Content-Length error, got: %v", err)
	}
}

func TestConn_CallAndResponse(t *testing.T) {
	var reqBuf bytes.Buffer
	c, pw, sent := pipeConn(&reqBuf)
	go c.readLoop() //nolint:errcheck

	respBody := `{"jsonrpc":"2.0","id":1,"result":{"hello":"world"}}`
	respFrame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(respBody), respBody)

	// Feed the response only after call has sent the request (pending[1] is registered).
	go func() {
		<-sent
		fmt.Fprint(pw, respFrame)
		pw.Close()
	}()

	var result map[string]string
	if err := c.call(context.Background(), "test/method", map[string]any{"x": 1}, &result); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if result["hello"] != "world" {
		t.Errorf("got %v", result)
	}

	// Verify the written request has Content-Length framing.
	req := reqBuf.String()
	if !strings.Contains(req, "Content-Length:") {
		t.Errorf("request missing Content-Length: %q", req)
	}
	if !strings.Contains(req, `"method":"test/method"`) {
		t.Errorf("request missing method: %q", req)
	}
}

func TestConn_CallRPCError(t *testing.T) {
	c, pw, sent := pipeConn(io.Discard)
	go c.readLoop() //nolint:errcheck

	errBody := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`
	errFrame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(errBody), errBody)

	go func() {
		<-sent
		fmt.Fprint(pw, errFrame)
		pw.Close()
	}()

	err := c.call(context.Background(), "unknown/method", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestConn_CallContextCancelled verifies that call() returns ctx.Err() when
// the context is cancelled before the LSP server responds.
func TestConn_CallContextCancelled(t *testing.T) {
	c, _, _ := pipeConn(io.Discard)
	go c.readLoop() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := c.call(ctx, "test/method", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// TestConn_CallConnectionClosed verifies that call() returns errConnClosed
// when readLoop exits (e.g. the LSP process died) before a response arrives.
func TestConn_CallConnectionClosed(t *testing.T) {
	c, pw, sent := pipeConn(io.Discard)
	go c.readLoop() //nolint:errcheck

	// Close the read side after the request is sent so readLoop sees EOF.
	go func() {
		<-sent
		pw.Close()
	}()

	err := c.call(context.Background(), "test/method", nil, nil)
	if !errors.Is(err, errConnClosed) {
		t.Errorf("expected errConnClosed, got: %v", err)
	}
}

// TestReadLoop_WindowWorkDoneProgressCreate verifies that a server-initiated
// window/workDoneProgress/create request is acknowledged with a null result.
// Without this acknowledgement, rust-analyzer suppresses subsequent $/progress
// notifications, which causes waitForIdle to time out instead of detecting
// indexing completion.
func TestReadLoop_WindowWorkDoneProgressCreate(t *testing.T) {
	pr, pw := io.Pipe()
	var wmu sync.Mutex
	var wbuf bytes.Buffer
	c := newConn(pr, writerFunc(func(p []byte) (int, error) {
		wmu.Lock()
		defer wmu.Unlock()
		return wbuf.Write(p)
	}), slog.New(slog.DiscardHandler))

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		_ = c.readLoop()
	}()

	body := `{"jsonrpc":"2.0","id":42,"method":"window/workDoneProgress/create","params":{"token":"x"}}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := fmt.Fprint(pw, frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	_ = pw.Close()
	<-loopDone

	wmu.Lock()
	out := wbuf.String()
	wmu.Unlock()

	if !strings.Contains(out, `"id":42`) {
		t.Errorf("response missing id 42: %q", out)
	}
	if !strings.Contains(out, `"result":null`) {
		t.Errorf("response missing null result: %q", out)
	}
}

func TestParseSymbols_DocumentSymbol(t *testing.T) {
	syms := []DocumentSymbol{
		{
			Name:           "Foo",
			Kind:           SymbolKindFunction,
			Range:          Range{Start: Position{0, 0}, End: Position{5, 0}},
			SelectionRange: Range{Start: Position{0, 5}, End: Position{0, 8}},
			Children: []DocumentSymbol{
				{
					Name:           "Bar",
					Kind:           SymbolKindMethod,
					Range:          Range{Start: Position{1, 0}, End: Position{3, 0}},
					SelectionRange: Range{Start: Position{1, 5}, End: Position{1, 8}},
				},
			},
		},
	}
	raw, _ := json.Marshal(syms)
	result, err := parseSymbols(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Top-level only — `Bar` is a child of `Foo` and must not appear as a
	// sibling. Foo's Children slice should still carry Bar so other code
	// paths can walk it if needed.
	if len(result) != 1 {
		t.Fatalf("expected 1 top-level symbol, got %d", len(result))
	}
	if result[0].Name != "Foo" {
		t.Errorf("expected Foo, got %q", result[0].Name)
	}
	if len(result[0].Children) != 1 || result[0].Children[0].Name != "Bar" {
		t.Errorf("expected Bar preserved as Foo's child, got %+v", result[0].Children)
	}
}

func TestParseSymbols_FlatSymbolInformationDropped(t *testing.T) {
	// Server ignored hierarchicalDocumentSymbolSupport and returned
	// SymbolInformation[]. Without parent info we cannot tell which entries
	// are top-level, so parseSymbols returns nothing rather than re-emit
	// nested-scope noise.
	si := []SymbolInformation{
		{
			Name:     "Foo",
			Kind:     SymbolKindFunction,
			Location: Location{URI: "file:///x.ts", Range: Range{Start: Position{0, 0}, End: Position{5, 0}}},
		},
	}
	raw, _ := json.Marshal(si)
	result, err := parseSymbols(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty (flat form dropped), got %d entries", len(result))
	}
}

func TestParseSymbols_Empty(t *testing.T) {
	result, err := parseSymbols(json.RawMessage("null"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}
