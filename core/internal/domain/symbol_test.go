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

func TestParseSymbolExclude(t *testing.T) {
	cases := []struct {
		in      string
		want    SymbolExclude
		wantErr bool
	}{
		{in: "getStaticProps", want: SymbolExclude{Pattern: "getStaticProps"}},
		{in: "getStatic*", want: SymbolExclude{Pattern: "getStatic*"}},
		{in: "function:getServerSideProps", want: SymbolExclude{Kind: "function", Pattern: "getServerSideProps"}},
		{in: "method:render", want: SymbolExclude{Kind: "method", Pattern: "render"}},
		{in: "", wantErr: true},
		{in: "function:", wantErr: true},
		{in: "bogus:foo", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseSymbolExclude(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseSymbolExclude(%q) = %+v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSymbolExclude(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSymbolExclude(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestIsSymbolExcluded(t *testing.T) {
	specs := []string{"getStaticProps", "function:getServerSideProps", "method:render*"}

	cases := []struct {
		name string
		kind SymbolKind
		want bool
	}{
		// Any-kind exact match.
		{"getStaticProps", "function", true},
		{"getStaticProps", "variable", true},
		// Function-kind constraint excludes other kinds.
		{"getServerSideProps", "function", true},
		{"getServerSideProps", "variable", false},
		// Method-kind glob.
		{"render", "method", true},
		{"renderAsync", "method", true},
		{"renderAsync", "function", false},
		// Non-matches.
		{"getStaticPaths", "function", false},
		{"unrelated", "method", false},
	}
	for _, c := range cases {
		got, err := IsSymbolExcluded(c.name, c.kind, specs)
		if err != nil {
			t.Errorf("IsSymbolExcluded(%q, %q): unexpected error %v", c.name, c.kind, err)
		}
		if got != c.want {
			t.Errorf("IsSymbolExcluded(%q, %q) = %v, want %v", c.name, c.kind, got, c.want)
		}
	}

	// Malformed spec returns an error but does not block matches in
	// later specs from succeeding.
	got, err := IsSymbolExcluded("getStaticProps", "function", []string{"bogus:foo", "getStaticProps"})
	if err == nil {
		t.Errorf("expected parse error for malformed spec, got nil")
	}
	if !got {
		t.Errorf("expected match against the second spec despite the first being malformed")
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
