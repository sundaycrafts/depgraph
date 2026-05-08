package domain

import "testing"

func TestIsExcluded(t *testing.T) {
	excludes := []string{"**/*_test.go", "vendor/**"}
	cases := []struct {
		path string
		want bool
	}{
		{"foo_test.go", true},
		{"a/b/foo_test.go", true},
		{"vendor/x.go", true},
		{"foo.go", false},
		{"src/foo.ts", false},
	}
	for _, c := range cases {
		got, err := IsExcluded(c.path, excludes)
		if err != nil {
			t.Errorf("IsExcluded(%q) returned err: %v", c.path, err)
		}
		if got != c.want {
			t.Errorf("IsExcluded(%q) = %v want %v", c.path, got, c.want)
		}
	}
}

func TestIsExcluded_InvalidPattern(t *testing.T) {
	// doublestar.PathMatch returns ErrBadPattern for unbalanced brackets.
	_, err := IsExcluded("foo.go", []string{"foo[unbalanced"})
	if err == nil {
		t.Error("expected error for invalid glob pattern, got nil")
	}
}

func TestMatchesAnyExt(t *testing.T) {
	cases := []struct {
		path string
		exts []string
		want bool
	}{
		{"foo.go", []string{".go"}, true},
		{"foo.ts", []string{".go", ".ts", ".tsx"}, true},
		{"foo.txt", []string{".go", ".ts"}, false},
		{"foo", []string{".go"}, false},
		{"a/b/foo.rs", []string{".rs"}, true},
	}
	for _, c := range cases {
		if got := MatchesAnyExt(c.path, c.exts); got != c.want {
			t.Errorf("MatchesAnyExt(%q, %v) = %v want %v", c.path, c.exts, got, c.want)
		}
	}
}
