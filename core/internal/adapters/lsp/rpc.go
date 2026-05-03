package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// errConnClosed is returned by call when the underlying LSP process exits
// or its stdout reaches EOF before a response arrives.
var errConnClosed = errors.New("LSP connection closed")

// message is a JSON-RPC 2.0 message (request, response, or notification).
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// conn manages a JSON-RPC 2.0 connection over stdin/stdout of an LSP process.
type conn struct {
	w       io.Writer
	scanner *bufio.Scanner
	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan message
	pendMu  sync.Mutex

	progMu        sync.Mutex
	progFlight    int           // number of in-flight $/progress tokens
	progBeganOnce sync.Once
	progBeganCh   chan struct{} // closed when the first $/progress begin is received

	done chan struct{} // closed when readLoop exits, unblocking pending callers

	logger *slog.Logger
}

func newConn(r io.Reader, w io.Writer, logger *slog.Logger) *conn {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	scanner.Split(splitLSP)

	c := &conn{
		w:           w,
		scanner:     scanner,
		pending:     make(map[int64]chan message),
		progBeganCh: make(chan struct{}),
		done:        make(chan struct{}),
		logger:      logger,
	}
	return c
}

// call sends a request and waits for the response.
//
// Returns ctx.Err() if ctx is cancelled before the response arrives, and
// errConnClosed if the underlying readLoop exits (e.g. the LSP process died).
func (c *conn) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)

	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	ch := make(chan message, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	cleanup := func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}

	if err := c.send(&message{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  rawParams,
	}); err != nil {
		cleanup()
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && resp.Result != nil {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-ctx.Done():
		cleanup()
		return ctx.Err()
	case <-c.done:
		cleanup()
		return errConnClosed
	}
}

// notify sends a notification (no response expected).
func (c *conn) notify(method string, params any) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	return c.send(&message{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
	})
}

func (c *conn) send(msg *message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return err
}

// readLoop reads incoming messages and dispatches responses to pending callers.
// It returns when the reader is exhausted or returns an error. On exit, c.done
// is closed so any in-flight call() invocations unblock with errConnClosed
// instead of deadlocking.
func (c *conn) readLoop() error {
	defer close(c.done)
	for c.scanner.Scan() {
		var msg message
		if err := json.Unmarshal(c.scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.ID != nil {
			c.pendMu.Lock()
			ch, ok := c.pending[*msg.ID]
			if ok {
				delete(c.pending, *msg.ID)
			}
			c.pendMu.Unlock()
			if ok {
				ch <- msg
			}
		} else if msg.Method == "$/progress" {
			var p struct {
				Value struct {
					Kind string `json:"kind"`
				} `json:"value"`
			}
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				var signalBegin bool
				c.progMu.Lock()
				switch p.Value.Kind {
				case "begin":
					c.progFlight++
					signalBegin = true
				case "end":
					if c.progFlight > 0 {
						c.progFlight--
					}
				}
				c.progMu.Unlock()
				if signalBegin {
					c.progBeganOnce.Do(func() { close(c.progBeganCh) })
				}
			}
		}
	}
	return c.scanner.Err()
}

// waitForIdle blocks until the server has completed all background indexing.
//
// Some language servers (e.g. rust-analyzer) perform initial indexing
// asynchronously and signal it via $/progress begin/end notifications.
// We wait up to maxStartupWait for the first begin; if none arrives within
// that window the server is assumed to be already idle and we return early.
// Once at least one begin has been seen we poll until progFlight drops to zero.
func (c *conn) waitForIdle(ctx context.Context) {
	const maxStartupWait = 30 * time.Second
	const poll = 200 * time.Millisecond

	c.logger.Debug("waiting for language server indexing to begin")

	// Phase 1: block until first $/progress begin (or timeout/cancel).
	select {
	case <-c.progBeganCh:
		c.logger.Info("indexing started")
	case <-time.After(maxStartupWait):
		c.logger.Debug("no indexing activity detected, assuming server is ready")
		return // no progress ever started; server is idle
	case <-ctx.Done():
		return
	}

	// Phase 2: poll until all in-flight progress tokens are resolved.
	for {
		c.progMu.Lock()
		idle := c.progFlight <= 0
		c.progMu.Unlock()
		if idle {
			c.logger.Info("indexing complete")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// splitLSP is a bufio.SplitFunc that reads LSP Content-Length framed messages.
func splitLSP(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	// Find header terminator \r\n\r\n
	headerEnd := strings.Index(string(data), "\r\n\r\n")
	if headerEnd < 0 {
		if atEOF {
			return 0, nil, fmt.Errorf("LSP: unexpected EOF in header")
		}
		return 0, nil, nil
	}

	header := string(data[:headerEnd])
	contentLen := -1
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLen, err = strconv.Atoi(val)
			if err != nil {
				return 0, nil, fmt.Errorf("LSP: invalid Content-Length: %w", err)
			}
		}
	}
	if contentLen < 0 {
		return 0, nil, fmt.Errorf("LSP: missing Content-Length header")
	}

	start := headerEnd + 4
	end := start + contentLen
	if end > len(data) {
		if atEOF {
			return 0, nil, fmt.Errorf("LSP: unexpected EOF in body")
		}
		return 0, nil, nil
	}

	return end, data[start:end], nil
}
