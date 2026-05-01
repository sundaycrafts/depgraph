package lsp

import "encoding/json"

// URI is a file:// URI string.
type URI = string

// InitializeParams is the params for the "initialize" request.
type InitializeParams struct {
	ProcessID    int              `json:"processId"`
	RootURI      URI              `json:"rootUri"`
	Capabilities ClientCapability `json:"capabilities"`
}

// ClientCapability is a minimal capability declaration.
type ClientCapability struct {
	TextDocument TextDocumentClientCapabilities `json:"textDocument"`
}

// TextDocumentClientCapabilities declares document symbol support.
type TextDocumentClientCapabilities struct {
	DocumentSymbol DocumentSymbolClientCapabilities `json:"documentSymbol"`
}

// DocumentSymbolClientCapabilities enables hierarchical symbols.
type DocumentSymbolClientCapabilities struct {
	HierarchicalDocumentSymbolSupport bool `json:"hierarchicalDocumentSymbolSupport"`
}

// InitializeResult is the response to "initialize".
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
}

// ServerCapabilities (only fields used in Phase 1).
type ServerCapabilities struct {
	DefinitionProvider bool `json:"definitionProvider"`
	ReferencesProvider bool `json:"referencesProvider"`
}

// TextDocumentIdentifier identifies an open document.
type TextDocumentIdentifier struct {
	URI URI `json:"uri"`
}

// DocumentSymbolParams is the params for "textDocument/documentSymbol".
type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// SymbolKind mirrors the LSP SymbolKind enum (only commonly used values).
type SymbolKind int

const (
	SymbolKindFile        SymbolKind = 1
	SymbolKindModule      SymbolKind = 2
	SymbolKindNamespace   SymbolKind = 3
	SymbolKindPackage     SymbolKind = 4
	SymbolKindClass       SymbolKind = 5
	SymbolKindMethod      SymbolKind = 6
	SymbolKindProperty    SymbolKind = 7
	SymbolKindField       SymbolKind = 8
	SymbolKindConstructor SymbolKind = 9
	SymbolKindEnum        SymbolKind = 10
	SymbolKindInterface   SymbolKind = 11
	SymbolKindFunction    SymbolKind = 12
	SymbolKindVariable    SymbolKind = 13
	SymbolKindConstant    SymbolKind = 14
	SymbolKindStruct      SymbolKind = 23
	SymbolKindTypeParam   SymbolKind = 26
)

// DocumentSymbol is a symbol in a document (hierarchical form).
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Kind           SymbolKind       `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children"`
}

// SymbolInformation is the flat (non-hierarchical) form.
type SymbolInformation struct {
	Name     string     `json:"name"`
	Kind     SymbolKind `json:"kind"`
	Location Location   `json:"location"`
}

// symbolResult is the union type returned by textDocument/documentSymbol.
// It may be []DocumentSymbol or []SymbolInformation.
type symbolResult = json.RawMessage

// Range is an LSP range (0-based lines, UTF-16 characters).
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Position is an LSP position.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Location is a file URI + range.
type Location struct {
	URI   URI   `json:"uri"`
	Range Range `json:"range"`
}

// DefinitionParams is the params for "textDocument/definition".
type DefinitionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// ReferenceParams is the params for "textDocument/references".
type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      ReferenceContext       `json:"context"`
}

// ReferenceContext controls whether declaration is included.
type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}
