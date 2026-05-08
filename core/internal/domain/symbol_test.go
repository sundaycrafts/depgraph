package domain

import (
	"testing"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

func TestSymbolID_RoundTrip(t *testing.T) {
	cases := []SymbolID{
		{Lang: lsploader.Go, RelPath: "internal/foo.go", Line: 12, Char: 5, Name: "Foo"},
		{Lang: lsploader.Rust, RelPath: "src/lib.rs", Line: 0, Char: 0, Name: "Trait::method"},
		{Lang: lsploader.TypeScript, RelPath: "src/x:y.ts", Line: 99, Char: 200, Name: "name with space"},
	}
	for _, want := range cases {
		got, err := DecodeSymbolID(EncodeSymbolID(want))
		if err != nil {
			t.Fatalf("decode failed for %#v: %v", want, err)
		}
		if got != want {
			t.Errorf("round-trip mismatch:\n  got  %#v\n  want %#v", got, want)
		}
	}
}

func TestSymbolID_DecodeRejectsMalformed(t *testing.T) {
	bad := []string{"", "only-three:parts:1", "go:foo.go:not-a-line:0:Zm9v"}
	for _, s := range bad {
		if _, err := DecodeSymbolID(s); err == nil {
			t.Errorf("expected error decoding %q, got nil", s)
		}
	}
}

func TestFuzzyMatch(t *testing.T) {
	cases := []struct {
		query, target string
		want          bool
	}{
		{"add", "add", true},
		{"ad", "add", true},
		{"ADD", "add", true},
		{"add", "ADD", true},
		{"", "anything", true},
		{"", "", true},
		{"abc", "aXbXc", true},
		{"zzz", "add", false},
		{"addd", "add", false},
	}
	for _, tc := range cases {
		if got := FuzzyMatch(tc.query, tc.target); got != tc.want {
			t.Errorf("FuzzyMatch(%q, %q) = %v, want %v", tc.query, tc.target, got, tc.want)
		}
	}
}

func TestRangeContains(t *testing.T) {
	r := Range{
		Start: Position{Line: 1, Character: 2},
		End:   Position{Line: 3, Character: 4},
	}
	cases := []struct {
		pos  Position
		want bool
	}{
		{Position{Line: 1, Character: 2}, true},
		{Position{Line: 1, Character: 1}, false},
		{Position{Line: 2, Character: 0}, true},
		{Position{Line: 3, Character: 3}, true},
		{Position{Line: 3, Character: 4}, false}, // exclusive end
		{Position{Line: 4, Character: 0}, false},
	}
	for _, c := range cases {
		if got := RangeContains(r, c.pos); got != c.want {
			t.Errorf("RangeContains(%v) = %v want %v", c.pos, got, c.want)
		}
	}
}

func TestInnermostSymbol(t *testing.T) {
	outer := DocumentSymbol{
		Name:           "Outer",
		Range:          Range{Start: Position{Line: 0}, End: Position{Line: 100}},
		SelectionRange: Range{Start: Position{Line: 0, Character: 5}},
		Children: []DocumentSymbol{
			{
				Name:           "Inner",
				Range:          Range{Start: Position{Line: 10}, End: Position{Line: 20}},
				SelectionRange: Range{Start: Position{Line: 10, Character: 2}},
			},
		},
	}
	got := InnermostSymbol([]DocumentSymbol{outer}, Position{Line: 15})
	if got == nil || got.Name != "Inner" {
		t.Errorf("expected Inner, got %v", got)
	}
	got = InnermostSymbol([]DocumentSymbol{outer}, Position{Line: 50})
	if got == nil || got.Name != "Outer" {
		t.Errorf("expected Outer, got %v", got)
	}
	got = InnermostSymbol([]DocumentSymbol{outer}, Position{Line: 200})
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestFlattenDocumentSymbols(t *testing.T) {
	tree := []DocumentSymbol{
		{Name: "A", Children: []DocumentSymbol{
			{Name: "A1"},
			{Name: "A2", Children: []DocumentSymbol{{Name: "A2a"}}},
		}},
		{Name: "B"},
	}
	got := FlattenDocumentSymbols(tree)
	wantNames := []string{"A", "A1", "A2", "A2a", "B"}
	if len(got) != len(wantNames) {
		t.Fatalf("len=%d want %d (%v)", len(got), len(wantNames), got)
	}
	for i, n := range wantNames {
		if got[i].Name != n {
			t.Errorf("got[%d]=%q want %q", i, got[i].Name, n)
		}
	}
}
