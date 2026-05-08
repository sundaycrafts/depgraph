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
	fswatchadapter "github.com/sundaycrafts/depgraph/internal/adapters/fswatch"
	httpadapter "github.com/sundaycrafts/depgraph/internal/adapters/http"
	lspadapter "github.com/sundaycrafts/depgraph/internal/adapters/lsp"
	mcpadapter "github.com/sundaycrafts/depgraph/internal/adapters/mcp"
	"github.com/sundaycrafts/depgraph/internal/domain"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
	"github.com/sundaycrafts/depgraph/internal/version"
)

func main() {
	parsed := parseArgs()
	root := parsed.root

	level := slog.LevelInfo
	if parsed.verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	slog.Info("depgraph starting", "version", version.Version, "mode", modeName(parsed.mcp))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var server domain.PortServer
	if parsed.mcp {
		cfg, err := lspadapter.LoadEmbeddedConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "load embedded LSP config: %v\n", err)
			os.Exit(1)
		}
		workspace := domain.NewWorkspace(
			ctx,
			lspadapter.NewSessionFactory(cfg, slog.Default()),
			fswatchadapter.NewFactory(slog.Default()),
			domain.PortLanguageDetectorFunc(lsploader.Detect),
			lspadapter.ExecLocator{},
			slog.Default(),
		)
		server = mcpadapter.New(workspace, mcpadapter.NewNoopInitializer(), slog.Default())
	} else {
		analyzer := lspadapter.New(
			lspadapter.WithExcludeGlobs(parsed.excludes...),
			lspadapter.WithLogger(slog.Default()),
		)

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
				fmt.Printf("depgraph %s — serving %s\n", version.Version, addr)
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

func modeName(mcp bool) string {
	if mcp {
		return "mcp"
	}
	return "http"
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
