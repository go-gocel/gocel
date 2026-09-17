package jsonx

import (
	"encoding/json"
	"testing"
)

// Regression: a stray full-width closing quote (” or ’) at structural
// position used to open a "string" that swallowed the JSON value — the
// extractor then reported "no parseable JSON" (C-series).
func TestParse_StrayFullwidthCloseQuote(t *testing.T) {
	var v map[string]int
	if err := Parse("他说：”然后输出 {\"a\": 1}", &v); err != nil {
		t.Fatalf("Parse with stray ”: %v", err)
	}
	if v["a"] != 1 {
		t.Fatalf("Parse with stray ”: got %v", v)
	}
}

func TestParse_StraySingleCloseQuote(t *testing.T) {
	var v []int
	if err := Parse("答案：’结果是 [1, 2]", &v); err != nil {
		t.Fatalf("Parse with stray ’: %v", err)
	}
	if len(v) != 2 || v[0] != 1 || v[1] != 2 {
		t.Fatalf("Parse with stray ’: got %v", v)
	}
}

// Regression: LenientBool trimmed whitespace before stripping quotes, so
// `" true "` (whitespace inside quotes) failed to parse.
func TestLenientBool_WhitespaceInsideQuotes(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`" true "`, true},
		{`"false "`, false},
		{`" 1 "`, true},
		{`" 0 "`, false},
		{`"true"`, true},
		{`true`, true},
		{`" "`, false},
	} {
		var b LenientBool
		if err := json.Unmarshal([]byte(tc.raw), &b); err != nil {
			t.Fatalf("Unmarshal(%s): %v", tc.raw, err)
		}
		if bool(b) != tc.want {
			t.Fatalf("Unmarshal(%s) = %v, want %v", tc.raw, bool(b), tc.want)
		}
	}
}
