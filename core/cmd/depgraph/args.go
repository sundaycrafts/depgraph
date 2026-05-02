package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// stringSlice collects repeatable flag values.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

type cliArgs struct {
	root     string
	excludes []string
	mcp      bool
}

func parseArgs() cliArgs {
	var excludes stringSlice
	var mcpMode bool
	flag.Var(&excludes, "exclude", "glob pattern relative to <target-dir> to exclude (repeatable)")
	flag.BoolVar(&mcpMode, "mcp", false, "run as MCP stdio server instead of HTTP server")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: depgraph <target-dir> [--exclude=<glob>]... [--mcp]")
		flag.PrintDefaults()
	}

	// flag.Parse stops at the first non-flag argument, so reorder argv to move
	// any "--flag=value" args before positional args before parsing.
	var flagArgs, posArgs []string
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
		} else {
			posArgs = append(posArgs, a)
		}
	}
	os.Args = append(os.Args[:1], append(flagArgs, posArgs...)...)
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	return cliArgs{
		root:     args[0],
		excludes: excludes,
		mcp:      mcpMode,
	}
}
