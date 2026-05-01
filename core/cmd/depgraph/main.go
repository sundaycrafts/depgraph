package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	fsadapter "github.com/sundaycrafts/depgraph/internal/adapters/fs"
	httpadapter "github.com/sundaycrafts/depgraph/internal/adapters/http"
	lspadapter "github.com/sundaycrafts/depgraph/internal/adapters/lsp"
)

var version = "dev"

// stringSlice collects repeatable flag values.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var excludes stringSlice
	flag.Var(&excludes, "exclude", "glob pattern relative to <target-dir> to exclude (repeatable)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: depgraph <target-dir> [--exclude <glob>]...")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}
	root := args[0]

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	analyzer := lspadapter.New(lspadapter.WithExcludeGlobs(excludes...))
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
