package domain

import "github.com/sundaycrafts/depgraph/internal/lsploader"

// PortLanguageDetector inspects a directory and reports which depgraph
// languages it carries. The current production implementation defers to
// `lsploader.Detect`, which checks for marker files (go.mod, Cargo.toml,
// tsconfig.json, pyproject.toml, etc.) at the directory root.
//
// Returning an empty slice (with nil error) means "no supported languages
// found"; the caller decides whether that is fatal.
type PortLanguageDetector interface {
	Detect(root string) ([]lsploader.Language, error)
}

// PortLanguageDetectorFunc adapts a function into a PortLanguageDetector.
type PortLanguageDetectorFunc func(root string) ([]lsploader.Language, error)

// Detect satisfies PortLanguageDetector.
func (f PortLanguageDetectorFunc) Detect(root string) ([]lsploader.Language, error) {
	return f(root)
}
