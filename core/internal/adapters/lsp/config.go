package lsp

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

//go:embed config.json
var configJSON []byte

// Config mirrors the zed-style configuration file embedded in config.json.
// Only the fields the LSP session factory currently consumes are decoded.
type Config struct {
	Languages map[string]LanguageConfig `json:"languages"`
	LSP       map[string]LSPConfig      `json:"lsp"`
}

type LanguageConfig struct {
	LanguageServers []string `json:"language_servers"`
}

type LSPConfig struct {
	Command               string          `json:"command"`
	Args                  []string        `json:"args"`
	InitializationOptions json.RawMessage `json:"initialization_options"`
}

// LoadEmbeddedConfig parses the embedded config.json.
func LoadEmbeddedConfig() (*Config, error) {
	var c Config
	if err := json.Unmarshal(configJSON, &c); err != nil {
		return nil, fmt.Errorf("parse embedded config: %w", err)
	}
	return &c, nil
}

// languageNames maps lsploader.Language values to the canonical name used as
// a key in the config.json languages map.
var languageNames = map[lsploader.Language]string{
	lsploader.Go:         "Go",
	lsploader.Rust:       "Rust",
	lsploader.TypeScript: "TypeScript",
	lsploader.Python:     "Python",
}

// LSPSpecsFor returns the LSPConfig entries that should be launched for the
// given language. It looks up languages.<name>.language_servers and joins
// each entry against lsp.<server>.
func (c *Config) LSPSpecsFor(lang lsploader.Language) ([]LSPConfig, error) {
	name, ok := languageNames[lang]
	if !ok {
		return nil, fmt.Errorf("language %q has no config entry", lang)
	}
	lc, ok := c.Languages[name]
	if !ok {
		return nil, fmt.Errorf("config: languages.%s missing", name)
	}
	specs := make([]LSPConfig, 0, len(lc.LanguageServers))
	for _, server := range lc.LanguageServers {
		spec, ok := c.LSP[server]
		if !ok {
			return nil, fmt.Errorf("config: lsp.%s missing for language %s", server, name)
		}
		if spec.Command == "" {
			spec.Command = server
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
