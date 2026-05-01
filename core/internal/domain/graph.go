package domain

type Graph struct {
	Nodes []Node
	Edges []Edge
}

type NodeKind string

const (
	NodeKindFile   NodeKind = "file"
	NodeKindSymbol NodeKind = "symbol"
)

type Node struct {
	ID         string
	Kind       NodeKind
	Label      string
	Path       string
	SymbolKind string // LSP symbol kind string; empty for file nodes
	Range      *Range
}

type EdgeKind string

const (
	EdgeKindDefines    EdgeKind = "defines"
	EdgeKindReferences EdgeKind = "references"
)

type Confidence string

const (
	ConfidenceExact    Confidence = "exact"
	ConfidenceProbable Confidence = "probable"
)

type Edge struct {
	ID         string
	From       string
	To         string
	Kind       EdgeKind
	Confidence Confidence
}

// Position follows LSP convention: 0-based line, UTF-16 character offset.
type Position struct {
	Line      int
	Character int
}

type Range struct {
	Start Position
	End   Position
}
