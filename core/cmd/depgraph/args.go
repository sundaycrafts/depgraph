package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sundaycrafts/depgraph/internal/version"
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
}

func parseArgs() cliArgs {
	// `depgraph version` prints the build version and exits before any flag
	// parsing, so it works without arguments and isn't shadowed by `--version`.
	if len(os.Args) >= 2 && os.Args[1] == "version" {
		fmt.Println(version.Version)
		os.Exit(0)
	}

	var excludes stringSlice
	var mcpMode bool
	var verbose bool
	flag.Var(&excludes, "exclude", "glob pattern relative to <target-dir> to exclude (repeatable)")
	flag.BoolVar(&mcpMode, "mcp", false, "run as MCP stdio server instead of HTTP server")
	flag.BoolVar(&verbose, "verbose", false, "enable verbose (debug-level) logging")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: depgraph <target-dir> [--exclude=<glob>]... [--mcp] [--verbose]")
		fmt.Fprintln(os.Stderr, "       depgraph version")
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
		// MCP mode: the agent registers components at runtime via add_component;
		// any positional argument is ignored. The CWD is auto-registered if it
		// contains a supported marker file (go.mod, Cargo.toml, tsconfig.json).
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
	}
}
