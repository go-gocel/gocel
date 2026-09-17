package tool

import (
	"context"
	"reflect"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
)

func noopToolFn(ctx context.Context) string { return "" }

// TestWithToolEffects_Declared reports declarations verbatim — read-only
// tools must be able to declare their (lack of) mutating classes.
func TestWithToolEffects_Declared(t *testing.T) {
	ft, err := ToolFromFunc(noopToolFn, WithToolName("ro"),
		WithToolEffects(kernel.EffectRead, kernel.EffectNetwork))
	if err != nil {
		t.Fatalf("ToolFromFunc: %v", err)
	}
	got := ft.ToolMeta().Effects
	want := []kernel.ToolEffect{kernel.EffectRead, kernel.EffectNetwork}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolMeta().Effects = %v, want %v", got, want)
	}
	if eff := kernel.EffectiveEffects(ft); !reflect.DeepEqual(eff, want) {
		t.Fatalf("EffectiveEffects = %v, want declared %v", eff, want)
	}
}

// TestWithToolEffects_AbsentKeepsConservativeFallback locks the fail-closed
// default: an undeclared FuncTool is still treated as write + exec.
func TestWithToolEffects_AbsentKeepsConservativeFallback(t *testing.T) {
	ft, err := ToolFromFunc(noopToolFn, WithToolName("undeclared"))
	if err != nil {
		t.Fatalf("ToolFromFunc: %v", err)
	}
	if ft.ToolMeta().Effects != nil {
		t.Fatalf("undeclared Effects = %v, want nil (conservative)", ft.ToolMeta().Effects)
	}
	eff := kernel.EffectiveEffects(ft)
	want := []kernel.ToolEffect{kernel.EffectWrite, kernel.EffectExec}
	if !reflect.DeepEqual(eff, want) {
		t.Fatalf("EffectiveEffects = %v, want conservative %v", eff, want)
	}
}

// TestWithToolEffects_CopiesInput guards against aliasing: later mutation of
// the caller's slice must not change the tool's declaration.
func TestWithToolEffects_CopiesInput(t *testing.T) {
	in := []kernel.ToolEffect{kernel.EffectRead}
	ft, err := ToolFromFunc(noopToolFn, WithToolName("aliased"), WithToolEffects(in...))
	if err != nil {
		t.Fatalf("ToolFromFunc: %v", err)
	}
	in[0] = kernel.EffectWrite
	if got := ft.ToolMeta().Effects[0]; got != kernel.EffectRead {
		t.Fatalf("declaration aliased caller slice: got %v", got)
	}
}
