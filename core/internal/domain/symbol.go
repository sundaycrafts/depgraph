package domain

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// SymbolKind is the human-readable name for an LSP SymbolKind enum value.
// We keep the LSP enum as the source of truth (mapping done by
// SymbolKindFromLSP) but surface a string everywhere downstream so the
// wire shape and test fixtures stay legible.
type SymbolKind string

// SymbolKindFromLSP maps the LSP SymbolKind integer to its canonical
// string form. Unknown values yield the empty string.
func SymbolKindFromLSP(k int) SymbolKind {
	switch k {
	case 1:
		return "file"
	case 2:
		return "module"
	case 3:
		return "namespace"
	case 4:
		return "package"
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "property"
	case 8:
		return "field"
	case 9:
		return "constructor"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "function"
	case 13:
		return "variable"
	case 14:
		return "constant"
	case 23:
		return "struct"
	case 26:
		return "typeParameter"
	default:
		return ""
	}
}

// DocumentSymbol mirrors LSP's textDocument/documentSymbol hierarchical
// result, expressed in domain terms. Adapters convert their wire types
// (e.g. lsp.LSPDocumentSymbol) into this shape before handing data to the
// domain layer.
type DocumentSymbol struct {
	Name           string
	Kind           SymbolKind
	Range          Range
	SelectionRange Range
	Children       []DocumentSymbol
}

// FlattenDocumentSymbols returns every entry in the tree (top-level plus
// every nested child) as a flat list. Preserves traversal order. Useful
// for find_symbols, where the agent searches by short name and should be
// able to find a method nested inside a class.
func FlattenDocumentSymbols(tree []DocumentSymbol) []DocumentSymbol {
	if len(tree) == 0 {
		return nil
	}
	out := make([]DocumentSymbol, 0, len(tree))
	for _, s := range tree {
		out = append(out, s)
		out = append(out, FlattenDocumentSymbols(s.Children)...)
	}
	return out
}

// InnermostSymbol returns the deepest entry whose Range contains pos, or
// nil if pos lies outside every symbol (e.g. a top-level statement).
// Used by find_references BFS to attribute a reference location to its
// containing function/method.
func InnermostSymbol(tree []DocumentSymbol, pos Position) *DocumentSymbol {
	var best *DocumentSymbol
	for i := range tree {
		if !RangeContains(tree[i].Range, pos) {
			continue
		}
		if child := InnermostSymbol(tree[i].Children, pos); child != nil {
			best = child
		} else {
			best = &tree[i]
		}
	}
	return best
}

// RangeContains reports whether r contains pos using LSP's half-open
// convention: start inclusive, end exclusive.
func RangeContains(r Range, pos Position) bool {
	if pos.Line < r.Start.Line || (pos.Line == r.Start.Line && pos.Character < r.Start.Character) {
		return false
	}
	if pos.Line > r.End.Line || (pos.Line == r.End.Line && pos.Character >= r.End.Character) {
		return false
	}
	return true
}

// SymbolExclude is a parsed `[kind:]pattern` spec used to skip
// individual symbols during graph construction or BFS traversal — e.g.
// to keep Next.js convention methods like `getStaticProps` from
// fanning out into every page-to-page edge.
type SymbolExclude struct {
	Kind    SymbolKind // empty ⇒ any kind matches
	Pattern string     // doublestar glob against Symbol.Name
}

// ParseSymbolExclude parses a `[kind:]pattern` spec. The first colon
// separates an optional kind prefix from the name pattern; LSP symbol
// names never contain a colon legally, so the split is unambiguous.
// Empty patterns and unknown kinds are rejected.
func ParseSymbolExclude(s string) (SymbolExclude, error) {
	if s == "" {
		return SymbolExclude{}, fmt.Errorf("empty exclude_symbol spec")
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		kind, pattern := s[:i], s[i+1:]
		if pattern == "" {
			return SymbolExclude{}, fmt.Errorf("exclude_symbol spec %q: empty pattern", s)
		}
		if !knownSymbolKind(kind) {
			return SymbolExclude{}, fmt.Errorf("exclude_symbol spec %q: unknown kind %q", s, kind)
		}
		return SymbolExclude{Kind: SymbolKind(kind), Pattern: pattern}, nil
	}
	return SymbolExclude{Pattern: s}, nil
}

// IsSymbolExcluded reports whether (name, kind) matches any spec in
// specs. Mirrors IsExcluded for file paths: malformed specs surface as
// the returned error so callers can warn-and-continue rather than
// abort. A match short-circuits and returns (true, firstErrSeenSoFar).
func IsSymbolExcluded(name string, kind SymbolKind, specs []string) (bool, error) {
	var firstErr error
	for _, raw := range specs {
		spec, err := ParseSymbolExclude(raw)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if spec.Kind != "" && spec.Kind != kind {
			continue
		}
		ok, err := doublestar.Match(spec.Pattern, name)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("pattern %q: %w", spec.Pattern, err)
			}
			continue
		}
		if ok {
			return true, firstErr
		}
	}
	return false, firstErr
}

// knownSymbolKind reports whether s is one of the canonical kind
// strings produced by SymbolKindFromLSP. Used to reject typos in
// exclude_symbol specs at parse time.
func knownSymbolKind(s string) bool {
	switch SymbolKind(s) {
	case "file", "module", "namespace", "package", "class", "method",
		"property", "field", "constructor", "enum", "interface",
		"function", "variable", "constant", "struct", "typeParameter":
		return true
	}
	return false
}

// FuzzyMatch reports whether all runes of query appear in target in order
// (case-insensitive). Empty query matches everything. Used to filter
// find_symbols results client-side.
func FuzzyMatch(query, target string) bool {
	query = strings.ToLower(query)
	target = strings.ToLower(target)
	qi := 0
	for _, c := range target {
		if qi < len(query) && rune(query[qi]) == c {
			qi++
		}
	}
	return qi == len(query)
}

// Symbol is the result unit returned by find_symbols and find_references.
// It is stateless: ID encodes everything needed to re-issue an LSP query
// without server-side bookkeeping.
type Symbol struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      SymbolKind `json:"kind"`
	Path      string     `json:"path"` // relative to the project root
	Line      int        `json:"line"`
	Character int        `json:"character"`
}

// SymbolID is the structured form encoded into Symbol.ID. The wire form
// is `<lang>:<base64(rel_path)>:<line>:<char>:<base64(name)>`. rel_path
// and name are base64-url encoded so any colon, path separator, or
// special character round-trips losslessly.
type SymbolID struct {
	Lang    lsploader.Language
	RelPath string
	Line    int
	Char    int
	Name    string
}

// EncodeSymbolID returns the wire form of id.
func EncodeSymbolID(id SymbolID) string {
	encPath := base64.RawURLEncoding.EncodeToString([]byte(id.RelPath))
	encName := base64.RawURLEncoding.EncodeToString([]byte(id.Name))
	return fmt.Sprintf("%s:%s:%d:%d:%s", id.Lang, encPath, id.Line, id.Char, encName)
}

// DecodeSymbolID parses a symbol_id produced by EncodeSymbolID. Returns
// an error if the format is unrecognisable; downstream tools surface the
// error to the caller.
func DecodeSymbolID(s string) (SymbolID, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 5 {
		return SymbolID{}, fmt.Errorf("invalid symbol_id %q: want 5 colon-separated parts", s)
	}
	pathBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return SymbolID{}, fmt.Errorf("invalid symbol_id %q: path: %w", s, err)
	}
	line, err := strconv.Atoi(parts[2])
	if err != nil {
		return SymbolID{}, fmt.Errorf("invalid symbol_id %q: line: %w", s, err)
	}
	char, err := strconv.Atoi(parts[3])
	if err != nil {
		return SymbolID{}, fmt.Errorf("invalid symbol_id %q: char: %w", s, err)
	}
	nameBytes, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		return SymbolID{}, fmt.Errorf("invalid symbol_id %q: name: %w", s, err)
	}
	return SymbolID{
		Lang:    lsploader.Language(parts[0]),
		RelPath: string(pathBytes),
		Line:    line,
		Char:    char,
		Name:    string(nameBytes),
	}, nil
}
