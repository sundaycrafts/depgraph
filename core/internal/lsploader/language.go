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
	MarkerFiles []string // presence of any at root signals this language
	FileExts    []string // source file extensions to collect for analysis
	LSPBinary   string   // language server executable name
	LSPArgs     []string // arguments passed to the language server process
	SkipDirs    []string // directory names to skip when walking the tree
	InstallHint string   // shown in error messages when the binary is missing
}

// ordered is the canonical iteration order for languages (deterministic output).
var ordered = []Language{Go, Rust, TypeScript}

var meta = map[Language]LanguageMeta{
	Go: {
		MarkerFiles: []string{"go.mod"},
		FileExts:    []string{".go"},
		LSPBinary:   "gopls",
		LSPArgs:     []string{"-mode=stdio"},
		SkipDirs:    []string{"vendor"},
		InstallHint: "go install golang.org/x/tools/gopls@latest",
	},
	Rust: {
		MarkerFiles: []string{"Cargo.toml"},
		FileExts:    []string{".rs"},
		LSPBinary:   "rust-analyzer",
		LSPArgs:     []string{},
		SkipDirs:    []string{"target"},
		InstallHint: "rustup component add rust-analyzer",
	},
	TypeScript: {
		MarkerFiles: []string{"tsconfig.json"},
		FileExts:    []string{".ts", ".tsx"},
		LSPBinary:   "typescript-language-server",
		LSPArgs:     []string{"--stdio"},
		SkipDirs:    []string{"node_modules"},
		InstallHint: "npm install -g typescript-language-server typescript",
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
