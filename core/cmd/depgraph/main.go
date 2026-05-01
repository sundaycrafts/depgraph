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
	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/ports"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: depgraph <target-dir>")
		os.Exit(1)
	}

	root := os.Args[1]
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// noopAnalyzer returns an empty graph; replaced when lsp branch is merged.
	analyzer := ports.AnalyzerFunc(func(_ context.Context, _ string) (domain.Graph, error) {
		return domain.Graph{Nodes: []domain.Node{}, Edges: []domain.Edge{}}, nil
	})

	editor := fsadapter.New(root)

	graph, err := analyzer.Analyze(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analysis failed: %v\n", err)
		os.Exit(1)
	}

	server := httpadapter.New(graph, editor)

	const addr = "http://localhost:8080"
	go func() {
		time.Sleep(300 * time.Millisecond)
		openBrowser(addr)
	}()

	fmt.Printf("depgraph %s — serving %s\n", version, addr)
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
