package toolutil

import "testing"

// Regression: GlobMatch's "**" handling compared the tail segments as
// literal string suffixes — "**/*.go" matched nothing (C9). These tests
// pin real recursive glob semantics.
func TestGlobMatch_DoubleStar(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Recursive "**" spanning zero or more path segments.
		{"**/*.go", "main.go", true},
		{"**/*.go", "src/main.go", true},
		{"**/*.go", "src/a/b/main.go", true},
		{"**/*.go", "main.txt", false},
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "src/a/b.ts", true},
		{"src/**/*.ts", "srcx/a.ts", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/xb", false},
		{"foo/**", "foo", true},
		{"foo/**", "foo/a/b", true},
		{"**", "anything/at/all", true},
		{"**/*", "x/y/z", true},
		// Non-"**" patterns keep filepath.Match semantics.
		{"*.go", "main.go", true},
		{"a/*.go", "a/b.go", true},
		{"a/*.go", "a/x/b.go", false},
		{"a/b.go", "a/b.go", true},
		{"a/b.go", "c/b.go", false},
	}
	for _, c := range cases {
		if got := GlobMatch(c.pattern, c.path); got != c.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
