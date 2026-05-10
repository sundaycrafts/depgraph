// Package config loads `depgraph.yaml` from the directory the depgraph
// process was launched in. The file pre-declares projects, excludes,
// and exclude_symbols so the MCP server can register them at startup —
// the agent no longer needs to issue add_project per session, and
// indexing kicks off before the first tool call.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Filename is the canonical config filename, looked up directly under
// the launch directory.
const Filename = "depgraph.yaml"

// Config is the parsed root of depgraph.yaml.
type Config struct {
	Projects []Project `yaml:"projects"`
}

// Project is one entry in the projects: array.
type Project struct {
	Root           string   `yaml:"root"`
	Excludes       []string `yaml:"excludes"`
	ExcludeSymbols []string `yaml:"exclude_symbols"`
}

// Load reads `depgraph.yaml` from cwd. Return contract:
//
//   - file absent: (nil, nil) — not an error, callers should fall back.
//   - YAML parse failure: (nil, [err]) — file present but unreadable.
//   - per-entry validation failure: (cfg with the offending entry
//     dropped, [warning]) — partial success.
//
// Roots are resolved to absolute paths via filepath.Abs anchored at
// cwd. Entries whose Root is empty, fails to resolve, or doesn't exist
// on disk are dropped with a warning so an isolated typo cannot crash
// the whole startup.
func Load(cwd string) (*Config, []string) {
	path := filepath.Join(cwd, Filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("%s: read failed: %v", Filename, err)}
	}

	var raw Config
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, []string{fmt.Sprintf("%s: parse failed: %v", Filename, err)}
	}

	cfg := &Config{}
	var warnings []string
	for i, p := range raw.Projects {
		root := p.Root
		if root == "" {
			warnings = append(warnings, fmt.Sprintf("%s: projects[%d]: root is empty, skipping entry", Filename, i))
			continue
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(cwd, root)
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: projects[%d]: root %q: %v, skipping entry", Filename, i, p.Root, err))
			continue
		}
		if info, err := os.Stat(abs); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: projects[%d]: root %q: %v, skipping entry", Filename, i, p.Root, err))
			continue
		} else if !info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("%s: projects[%d]: root %q is not a directory, skipping entry", Filename, i, p.Root))
			continue
		}
		cfg.Projects = append(cfg.Projects, Project{
			Root:           abs,
			Excludes:       append([]string(nil), p.Excludes...),
			ExcludeSymbols: append([]string(nil), p.ExcludeSymbols...),
		})
	}
	return cfg, warnings
}
