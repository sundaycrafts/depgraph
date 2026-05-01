package main

import (
	"context"
	"fmt"
	"os"

	fsadapter "github.com/sundaycrafts/depgraph/internal/adapters/fs"
	lspadapter "github.com/sundaycrafts/depgraph/internal/adapters/lsp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: depgraph <target-dir>")
		os.Exit(1)
	}

	root := os.Args[1]
	ctx := context.Background()

	analyzer := lspadapter.New()
	_ = fsadapter.New(root)

	graph, err := analyzer.Analyze(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analysis failed: %v\n", err)
		os.Exit(1)
	}

	_ = graph
	// TODO: wire HTTP server adapter and call server.Serve(ctx, graph)
}
