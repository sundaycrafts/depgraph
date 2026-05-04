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
	verbose  bool
	noCache  bool
}

func parseArgs() cliArgs {
	var excludes stringSlice
	var mcpMode bool
	var verbose bool
	var noCache bool
	flag.Var(&excludes, "exclude", "glob pattern relative to <target-dir> to exclude (repeatable)")
	flag.BoolVar(&mcpMode, "mcp", false, "run as MCP stdio server instead of HTTP server")
	flag.BoolVar(&verbose, "verbose", false, "enable verbose (debug-level) logging")
	flag.BoolVar(&noCache, "no-cache", false, "bypass the on-disk graph cache and re-run analysis")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: depgraph <target-dir> [--exclude=<glob>]... [--mcp] [--verbose] [--no-cache]")
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
	root := ""
	if mcpMode {
		// target dir is specified at runtime via the set_root MCP tool; positional arg is ignored
	} else if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	} else {
		root = args[0]
	}

	return cliArgs{
		root:     root,
		excludes: excludes,
		mcp:      mcpMode,
		verbose:  verbose,
		noCache:  noCache,
	}
}
