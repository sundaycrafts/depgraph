package lsp

import (
	"testing"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

func TestEmbeddedConfig_AllLanguagesPresent(t *testing.T) {
	cfg, err := LoadEmbeddedConfig()
	if err != nil {
		t.Fatalf("LoadEmbeddedConfig: %v", err)
	}
	for _, lang := range lsploader.All() {
		specs, err := cfg.LSPSpecsFor(lang)
		if err != nil {
			t.Errorf("LSPSpecsFor(%s): %v", lang, err)
			continue
		}
		if len(specs) == 0 {
			t.Errorf("language %s has no LSP servers configured", lang)
		}
		for _, spec := range specs {
			if spec.Command == "" {
				t.Errorf("language %s: LSP spec has empty Command: %#v", lang, spec)
			}
		}
	}
}
