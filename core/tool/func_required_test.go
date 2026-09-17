package tool

import (
	"context"
	"strings"
	"testing"
)

// TestRequiredArg_EmptyArgsMustFail is the C6 regression: a tool with
// required arguments must reject empty/absent arguments instead of silently
// zero-filling them.
func TestRequiredArg_EmptyArgsMustFail(t *testing.T) {
	tool := MustToolFromFunc(
		func(ctx context.Context, name string) (string, error) { return "hi " + name, nil },
		WithToolName("greet"),
		WithToolDescription("greet someone"),
	)

	if _, err := tool.Run(context.Background(), "{}"); err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("Run({}) = %v, want missing-required error", err)
	}
	if _, err := tool.Run(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("Run(\"\") = %v, want missing-required error", err)
	}
}

// TestRequiredArg_ExplicitNullMustFail pins the same contract for JSON null:
// "required" means a non-null value is present.
func TestRequiredArg_ExplicitNullMustFail(t *testing.T) {
	tool := MustToolFromFunc(
		func(ctx context.Context, name string) (string, error) { return "hi " + name, nil },
		WithToolName("greet"),
		WithToolDescription("greet someone"),
	)

	if _, err := tool.Run(context.Background(), `{"name":null}`); err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("Run({name:null}) = %v, want missing-required error", err)
	}
}

// TestStructArgs_EmptyArgsPass pins the non-regression side: struct-param
// tools with omitempty fields keep their zero-fill behavior for empty
// arguments (struct optionality is schema-level, not runtime-required).
func TestStructArgs_EmptyArgsPass(t *testing.T) {
	type opts struct {
		A string `json:"a,omitempty"`
		B string `json:"b,omitempty"`
	}
	optional := MustToolFromFunc(
		func(ctx context.Context, o opts) (string, error) { return o.A + o.B, nil },
		WithToolName("concat"),
		WithToolDescription("concat"),
	)
	out, err := optional.Run(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Run({}) on struct tool = %v, want nil", err)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty concat", out)
	}
}
