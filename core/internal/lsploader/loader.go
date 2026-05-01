package lsploader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Locator abstracts binary lookup so core logic can be tested without touching
// the real PATH or filesystem.
type Locator interface {
	LookupBinary(name string) (string, error)
}

// Detect returns the languages present in root by checking for language-specific
// marker files (go.mod, Cargo.toml, tsconfig.json) directly at the root level.
// Results are returned in canonical language order.
func Detect(root string) ([]Language, error) {
	var found []Language
	for _, lang := range ordered {
		m := meta[lang]
		for _, marker := range m.MarkerFiles {
			if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
				found = append(found, lang)
				break
			}
		}
	}
	return found, nil
}

// Check verifies that the LSP binary for each language is reachable via loc.
// All missing binaries are reported together in a single user-readable error
// that includes install hints.
func Check(loc Locator, langs []Language) error {
	var lines []string
	for _, lang := range langs {
		m := meta[lang]
		if _, err := loc.LookupBinary(m.LSPBinary); err != nil {
			lines = append(lines, fmt.Sprintf("  %s: %q not found — install with: %s",
				lang, m.LSPBinary, m.InstallHint))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return fmt.Errorf("required language servers not found:\n%s", strings.Join(lines, "\n"))
}
