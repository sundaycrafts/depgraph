package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	fsadapter "github.com/sundaycrafts/depgraph/internal/adapters/fs"
	httpadapter "github.com/sundaycrafts/depgraph/internal/adapters/http"
	lspadapter "github.com/sundaycrafts/depgraph/internal/adapters/lsp"
)

var version = "dev"

func main() {
	parsed := parseArgs()
	root := parsed.root

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	analyzer := lspadapter.New(lspadapter.WithExcludeGlobs(parsed.excludes...))
	editor := fsadapter.New(root)

	graph, err := analyzer.Analyze(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analysis failed: %v\n", err)
		os.Exit(1)
	}

	server := httpadapter.New(graph, editor,
		httpadapter.WithOnReady(func(addr string) {
			fmt.Printf("depgraph %s — serving %s\n", version, addr)
			go func() {
				time.Sleep(300 * time.Millisecond)
				openBrowser(addr)
			}()
		}),
	)

	if err := server.Serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	default:
		return
	}
	exec.Command(cmd, args...).Start() //nolint:errcheck
}
