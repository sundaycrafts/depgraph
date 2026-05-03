package lsploader

// Language represents a supported programming language.
type Language string

const (
	Go         Language = "go"
	Rust       Language = "rust"
	TypeScript Language = "typescript"
)

// LanguageMeta holds static configuration for a language's LSP toolchain.
type LanguageMeta struct {
	MarkerFiles     []string // presence of any at root signals this language
	FileExts        []string // source file extensions to collect for analysis
	LSPBinary       string   // language server executable name
	LSPArgs         []string // arguments passed to the language server process
	InstallHint     string   // shown in error messages when the binary is missing
	DefaultExcludes []string // doublestar glob patterns excluded from the walk
	// in addition to user-supplied --exclude flags
}

// ordered is the canonical iteration order for languages (deterministic output).
var ordered = []Language{Go, Rust, TypeScript}

var meta = map[Language]LanguageMeta{
	Go: {
		MarkerFiles:     []string{"go.mod"},
		FileExts:        []string{".go"},
		LSPBinary:       "gopls",
		LSPArgs:         []string{"-mode=stdio"},
		InstallHint:     "go install golang.org/x/tools/gopls@latest",
		DefaultExcludes: []string{"vendor/**"},
	},
	Rust: {
		MarkerFiles:     []string{"Cargo.toml"},
		FileExts:        []string{".rs"},
		LSPBinary:       "rust-analyzer",
		LSPArgs:         []string{},
		InstallHint:     "rustup component add rust-analyzer",
		DefaultExcludes: []string{"target/**"},
	},
	TypeScript: {
		MarkerFiles:     []string{"tsconfig.json"},
		FileExts:        []string{".ts", ".tsx"},
		LSPBinary:       "typescript-language-server",
		LSPArgs:         []string{"--stdio"},
		InstallHint:     "npm install -g typescript-language-server typescript",
		DefaultExcludes: []string{"node_modules/**"},
	},
}

// Meta returns the configuration for the given language.
func Meta(lang Language) LanguageMeta {
	return meta[lang]
}

// All returns all supported languages in canonical order.
func All() []Language {
	return ordered
}
