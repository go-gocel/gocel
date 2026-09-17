package runtime

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// stubEffectTool declares EffectWrite so hooks can observe the filled
// effects at the tool-call boundary.
type stubEffectTool struct{}

func (stubEffectTool) Name() string           { return "stub_write" }
func (stubEffectTool) Description() string    { return "stub" }
func (stubEffectTool) Schema() map[string]any { return nil }
func (stubEffectTool) Run(context.Context, string) (string, error) {
	return "ok", nil
}
func (stubEffectTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{Effects: []kernel.ToolEffect{kernel.EffectWrite}}
}

func TestToolCallHooks_ReceiveDeclaredEffects(t *testing.T) {
	reg := tool.NewMapToolRegistry([]kernel.Tool{stubEffectTool{}})
	rt := NewRuntime(nil, reg)

	var got []kernel.ToolEffect
	rt.OnToolCall(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		got = info.Effects
		return ctx, info, nil
	})

	rt.ExecTools(context.Background(), []*types.ToolCall{{
		Type:     "function",
		ID:       "c1",
		Function: types.ToolCallFunction{Name: "stub_write", Arguments: "{}"},
	}})

	if len(got) != 1 || got[0] != kernel.EffectWrite {
		t.Fatalf("hook effects = %v, want [EffectWrite]", got)
	}
}

// stubPlainTool declares nothing: hooks must observe the conservative
// default (write + exec) — fail-closed for undeclared tools.
type stubPlainTool struct{}

func (stubPlainTool) Name() string           { return "stub_plain" }
func (stubPlainTool) Description() string    { return "stub" }
func (stubPlainTool) Schema() map[string]any { return nil }
func (stubPlainTool) Run(context.Context, string) (string, error) {
	return "ok", nil
}
func (stubPlainTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{} }

func TestToolCallHooks_UndeclaredEffectsFallClosed(t *testing.T) {
	reg := tool.NewMapToolRegistry([]kernel.Tool{stubPlainTool{}})
	rt := NewRuntime(nil, reg)

	var got []kernel.ToolEffect
	rt.OnToolCall(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		got = info.Effects
		return ctx, info, nil
	})

	rt.ExecTools(context.Background(), []*types.ToolCall{{
		Type:     "function",
		ID:       "c1",
		Function: types.ToolCallFunction{Name: "stub_plain", Arguments: "{}"},
	}})

	if len(got) != 2 || got[0] != kernel.EffectWrite || got[1] != kernel.EffectExec {
		t.Fatalf("hook effects = %v, want conservative [EffectWrite EffectExec]", got)
	}
}
