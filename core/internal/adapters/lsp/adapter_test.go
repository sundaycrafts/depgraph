package lsp

import (
	"testing"
)

func TestContainsPos_HalfOpen(t *testing.T) {
	r := Range{Start: Position{Line: 1, Character: 4}, End: Position{Line: 3, Character: 5}}
	tests := []struct {
		name string
		pos  Position
		want bool
	}{
		{"before start line", Position{0, 100}, false},
		{"on start exact", Position{1, 4}, true},
		{"just before start char", Position{1, 3}, false},
		{"middle line", Position{2, 0}, true},
		{"end line just before end char", Position{3, 4}, true},
		{"end line at end char (exclusive)", Position{3, 5}, false},
		{"end line past end char", Position{3, 6}, false},
		{"after end line", Position{4, 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsPos(r, tt.pos); got != tt.want {
				t.Errorf("containsPos(%v) = %v, want %v", tt.pos, got, tt.want)
			}
		})
	}
}

func TestFindContainingSymbol_PicksInnermost(t *testing.T) {
	outer := DocumentSymbol{
		Name:  "Outer",
		Range: Range{Start: Position{0, 0}, End: Position{20, 0}},
	}
	inner := DocumentSymbol{
		Name:  "Inner",
		Range: Range{Start: Position{5, 0}, End: Position{10, 0}},
	}
	disjoint := DocumentSymbol{
		Name:  "Disjoint",
		Range: Range{Start: Position{30, 0}, End: Position{40, 0}},
	}
	entries := []symEntry{
		{id: "outer", sym: outer},
		{id: "inner", sym: inner},
		{id: "disjoint", sym: disjoint},
	}

	if got := findContainingSymbol(entries, Position{Line: 7, Character: 3}); got != "inner" {
		t.Errorf("position inside inner: got %q, want inner", got)
	}
	if got := findContainingSymbol(entries, Position{Line: 15, Character: 3}); got != "outer" {
		t.Errorf("position only inside outer: got %q, want outer", got)
	}
	if got := findContainingSymbol(entries, Position{Line: 35, Character: 0}); got != "disjoint" {
		t.Errorf("position inside disjoint: got %q, want disjoint", got)
	}
	if got := findContainingSymbol(entries, Position{Line: 25, Character: 0}); got != "" {
		t.Errorf("position outside all: got %q, want empty", got)
	}
}

func TestFindContainingSymbol_EmptyEntries(t *testing.T) {
	if got := findContainingSymbol(nil, Position{1, 1}); got != "" {
		t.Errorf("nil entries: got %q, want empty", got)
	}
	if got := findContainingSymbol([]symEntry{}, Position{1, 1}); got != "" {
		t.Errorf("empty entries: got %q, want empty", got)
	}
}

func TestURIRoundTrip(t *testing.T) {
	cases := []string{
		"/home/ubuntu/depgraph/core/internal/adapters/lsp/adapter.go",
		"/tmp/foo bar/baz.go",
		"/a/b/c.go",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			got := uriToPath(fileURI(p))
			if got != p {
				t.Errorf("roundtrip %q -> %q -> %q", p, fileURI(p), got)
			}
		})
	}
}

func TestCanonPath_NormalizesEquivalentForms(t *testing.T) {
	a := canonPath("/tmp/./foo/bar.go")
	b := canonPath("/tmp/foo/bar.go")
	if a != b {
		t.Errorf("canonPath should collapse /./: a=%q b=%q", a, b)
	}

	a = canonPath("/tmp/foo//bar.go")
	if a != b {
		t.Errorf("canonPath should collapse //: a=%q b=%q", a, b)
	}

	a = canonPath("/tmp/foo/baz/../bar.go")
	if a != b {
		t.Errorf("canonPath should resolve ../: a=%q b=%q", a, b)
	}
}
