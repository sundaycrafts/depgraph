package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	fsadapter "github.com/sundaycrafts/depgraph/internal/adapters/fs"
	httpadapter "github.com/sundaycrafts/depgraph/internal/adapters/http"
	lspadapter "github.com/sundaycrafts/depgraph/internal/adapters/lsp"
	mcpadapter "github.com/sundaycrafts/depgraph/internal/adapters/mcp"
	"github.com/sundaycrafts/depgraph/internal/cache"
	"github.com/sundaycrafts/depgraph/internal/ports"
)

var version = "dev"

func main() {
	parsed := parseArgs()
	root := parsed.root

	level := slog.LevelInfo
	if parsed.verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var analyzer ports.AnalyzerPort = lspadapter.New(
		lspadapter.WithExcludeGlobs(parsed.excludes...),
		lspadapter.WithLogger(slog.Default()),
	)
	cacheOpts := []cache.Option{
		cache.WithVersion(version),
		cache.WithExcludes(parsed.excludes),
		cache.WithLogger(slog.Default()),
	}
	if parsed.noCache {
		cacheOpts = append(cacheOpts, cache.WithDisabled())
	}
	analyzer = cache.New(analyzer, cacheOpts...)

	var server ports.ServerPort
	if parsed.mcp {
		server = mcpadapter.New(analyzer, func(root string) ports.EditorPort {
			return fsadapter.New(root)
		})
	} else {
		editor := fsadapter.New(root)

		slog.Info("analyzing", "root", root)
		analyzeStart := time.Now()
		graph, err := analyzer.Analyze(ctx, root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "analysis failed: %v\n", err)
			os.Exit(1)
		}
		slog.Info("analysis complete",
			"nodes", len(graph.Nodes),
			"edges", len(graph.Edges),
			"elapsed", time.Since(analyzeStart),
		)

		server = httpadapter.New(graph, editor,
			httpadapter.WithOnReady(func(addr string) {
				fmt.Printf("depgraph %s — serving %s\n", version, addr)
				go func() {
					time.Sleep(300 * time.Millisecond)
					openBrowser(addr)
				}()
			}),
		)
	}

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
