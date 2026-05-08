package mcp

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// SymbolID is the stateless identifier returned by find_symbols and consumed
// by find_references. It encodes everything required to re-issue an LSP
// textDocument/references query without server-side state.
type SymbolID struct {
	Lang    lsploader.Language
	RelPath string
	Line    int
	Char    int
	Name    string
}

// EncodeSymbolID returns the wire form of id. Format:
//
//	<lang>:<base64(rel_path)>:<line>:<char>:<base64(name)>
//
// rel_path and name are base64-url encoded so any path separator, colon, or
// special character inside them survives the round trip. Lang values are
// constrained to lowercase ASCII identifiers in lsploader, so they're safe
// to emit verbatim.
func EncodeSymbolID(id SymbolID) string {
	encPath := base64.RawURLEncoding.EncodeToString([]byte(id.RelPath))
	encName := base64.RawURLEncoding.EncodeToString([]byte(id.Name))
	return fmt.Sprintf("%s:%s:%d:%d:%s", id.Lang, encPath, id.Line, id.Char, encName)
}

// DecodeSymbolID parses a symbol_id produced by EncodeSymbolID. Returns an
// error if the format is unrecognisable; downstream tools surface the error
// to the caller.
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
