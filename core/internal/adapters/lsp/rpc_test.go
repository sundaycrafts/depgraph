package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
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
	}))
	return c, pw, sent
}

func TestSplitLSP(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{}}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)

	advance, token, err := splitLSP([]byte(frame), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if advance != len(frame) {
		t.Errorf("advance=%d, want %d", advance, len(frame))
	}
	if string(token) != body {
		t.Errorf("token=%q, want %q", token, body)
	}
}

func TestSplitLSP_Partial(t *testing.T) {
	body := `{"jsonrpc":"2.0"}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body)+10, body)

	advance, token, err := splitLSP([]byte(frame), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if advance != 0 || token != nil {
		t.Errorf("expected no progress on partial body, got advance=%d token=%q", advance, token)
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
	if err := c.call("test/method", map[string]any{"x": 1}, &result); err != nil {
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

	err := c.call("unknown/method", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("unexpected error message: %v", err)
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
	if len(result) != 2 {
		t.Errorf("expected 2 flattened symbols, got %d", len(result))
	}
	if result[0].Name != "Foo" || result[1].Name != "Bar" {
		t.Errorf("unexpected names: %v, %v", result[0].Name, result[1].Name)
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
