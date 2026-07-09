package lsploader

// Language represents a supported programming language.
type Language string

const (
	Go         Language = "go"
	Rust       Language = "rust"
	TypeScript Language = "typescript"
	Python     Language = "python"
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

	// PreloadFiles marks language servers that only answer document and
	// cross-file queries for files in their open set (didOpen), so live
	// sessions must bulk-open every project file up front. gopls and
	// rust-analyzer answer from disk; tsserver and pyright do not.
	PreloadFiles bool
}

// ordered is the canonical iteration order for languages (deterministic output).
var ordered = []Language{Go, Rust, TypeScript, Python}

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
		PreloadFiles:    true,
	},
	Python: {
		MarkerFiles:     []string{"pyproject.toml", "setup.py", "requirements.txt"},
		FileExts:        []string{".py"},
		LSPBinary:       "pyright-langserver",
		LSPArgs:         []string{"--stdio"},
		InstallHint:     "npm install -g pyright",
		DefaultExcludes: []string{"venv/**", "__pycache__/**"},
		PreloadFiles:    true,
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
