package lsp

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// stderrTail is a thread-safe ring buffer of the most recent N lines a language
// server wrote to stderr. The tail is surfaced at ERROR level when the server
// dies unexpectedly so the cause (panic, OOM, etc.) is captured in our logs.
type stderrTail struct {
	mu    sync.Mutex
	lines []string
	pos   int
	full  bool
}

func newStderrTail(capacity int) *stderrTail {
	return &stderrTail{lines: make([]string, capacity)}
}

func (t *stderrTail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines[t.pos] = line
	t.pos = (t.pos + 1) % len(t.lines)
	if t.pos == 0 {
		t.full = true
	}
}

func (t *stderrTail) snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.full {
		return strings.Join(t.lines[:t.pos], "\n")
	}
	parts := make([]string, 0, len(t.lines))
	parts = append(parts, t.lines[t.pos:]...)
	parts = append(parts, t.lines[:t.pos]...)
	return strings.Join(parts, "\n")
}

// shutdownLSP performs the LSP shutdown handshake (`shutdown` request +
// `exit` notification), waits up to 5 seconds for the process to exit, and
// falls back to SIGKILL if it doesn't. Always calls cmd.Wait so the kernel
// reaps the process — without this the subprocess becomes a zombie. If the
// server exits abnormally, the captured stderr tail is emitted at ERROR level.
func shutdownLSP(cmd *exec.Cmd, c *conn, tail *stderrTail, stderrDone <-chan struct{}, logger *slog.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	_ = c.call(shutdownCtx, "shutdown", nil, nil)
	cancel()
	_ = c.notify("exit", map[string]any{})

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var waitErr error
	killed := false
	select {
	case waitErr = <-waitCh:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		waitErr = <-waitCh
		killed = true
	}

	// Drain the stderr goroutine. cmd.Wait closes the pipe so it should already
	// be done; the timeout is purely defensive against any edge case.
	select {
	case <-stderrDone:
	case <-time.After(time.Second):
	}

	switch {
	case killed:
		logger.Error("language server killed after shutdown timeout",
			"err", waitErr, "stderr_tail", tail.snapshot())
	case waitErr != nil:
		logger.Error("language server exited abnormally",
			"err", waitErr, "stderr_tail", tail.snapshot())
	default:
		logger.Debug("language server exited cleanly")
	}
}
