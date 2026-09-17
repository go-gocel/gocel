package runtime_test

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

// TestCallModelAllowsSingleSystem 确认合法列表（单 system 或无 system）不被误伤。
func TestCallModelAllowsSingleSystem(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	msgs := []*types.Message{types.NewSystemMessage("first"), types.NewUserMessage("hi")}
	if _, _, err := rt.CallModel(context.Background(), msgs); err != nil {
		t.Fatalf("single system rejected: %v", err)
	}
}

// TestFireMessagesBuiltCollapsesSystem 确认钩子追加的第 2 条 system
// 被 FireMessagesBuilt 合并进第 0 条（不再 fail-fast）。
func TestFireMessagesBuiltCollapsesSystem(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	rt.OnMessagesBuilt(func(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
		return ctx, append(msgs, types.NewSystemMessage("injected")), nil
	})
	msgs := []*types.Message{types.NewSystemMessage("first"), types.NewUserMessage("hi")}
	_, out, err := rt.FireMessagesBuilt(context.Background(), msgs)
	if err != nil {
		t.Fatalf("FireMessagesBuilt: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (collapsed)", len(out))
	}
	if out[0].Role != types.RoleSystem || out[0].Content != "first\n\ninjected" {
		t.Fatalf("out[0] = %+v, want collapsed system", out[0])
	}
	if out[1].Role != types.RoleUser || out[1].Content != "hi" {
		t.Fatalf("out[1] = %+v, want user", out[1])
	}
}
