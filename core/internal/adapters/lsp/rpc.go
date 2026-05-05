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
	br      *bufio.Reader
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
	return &conn{
		w:           w,
		br:          bufio.NewReader(r),
		pending:     make(map[int64]chan message),
		progBeganCh: make(chan struct{}),
		done:        make(chan struct{}),
		logger:      logger,
	}
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

// readMessage reads one LSP-framed message body from c.br. The LSP base
// protocol prefixes each message with `Content-Length: N\r\n\r\n`, so we
// parse headers line by line and then read exactly N body bytes — there is
// no per-message size limit. Returns io.EOF when the stream is cleanly
// exhausted at a message boundary.
func (c *conn) readMessage() ([]byte, error) {
	contentLen := -1
	for {
		line, err := c.br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("LSP: invalid Content-Length: %w", err)
			}
			contentLen = n
		}
	}
	if contentLen < 0 {
		return nil, fmt.Errorf("LSP: missing Content-Length header")
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(c.br, body); err != nil {
		return nil, fmt.Errorf("LSP: read body: %w", err)
	}
	return body, nil
}

// readLoop reads incoming messages and dispatches responses to pending callers.
// It returns when the reader is exhausted or returns an error. On exit, c.done
// is closed so any in-flight call() invocations unblock with errConnClosed
// instead of deadlocking.
func (c *conn) readLoop() error {
	defer close(c.done)
	for {
		body, err := c.readMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var msg message
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		if msg.ID != nil && msg.Method != "" {
			// Server-initiated request. Acknowledge the ones we know about so
			// the server can proceed; ignore others. rust-analyzer suppresses
			// $/progress notifications until window/workDoneProgress/create is
			// acknowledged, which is exactly what waitForIdle relies on.
			if msg.Method == "window/workDoneProgress/create" {
				_ = c.send(&message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage("null")})
			}
		} else if msg.ID != nil {
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
	var indexStart time.Time
	select {
	case <-c.progBeganCh:
		indexStart = time.Now()
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
			c.logger.Info("indexing complete", "elapsed", time.Since(indexStart))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

