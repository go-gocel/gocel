package engine

import (
	"context"
	"errors"
	"testing"

	coreengine "github.com/go-gocel/gocel/core/engine"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// scriptedModel returns a tool call on its first Generate and a final answer
// on subsequent calls — the minimal ReAct trace.
type scriptedModel struct {
	calls int
}

func (m *scriptedModel) Generate(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	m.calls++
	if m.calls == 1 {
		return &types.Message{
			Role: types.RoleAssistant,
			ToolCalls: []types.ToolCall{{
				ID:       "call-1",
				Type:     "function",
				Function: types.ToolCallFunction{Name: "echo", Arguments: `{"text":"hi"}`},
			}},
		}, &types.TokenUsage{TotalTokens: 1}, nil
	}
	return types.NewAssistantMessage("final answer"), &types.TokenUsage{TotalTokens: 2}, nil
}

func (m *scriptedModel) Stream(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}

func (m *scriptedModel) CountTokens(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (int, error) {
	return 0, nil
}

// alwaysToolModel never stops calling tools — the step budget must stop the loop.
type alwaysToolModel struct{}

func (alwaysToolModel) Generate(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return &types.Message{
		Role: types.RoleAssistant,
		ToolCalls: []types.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: types.ToolCallFunction{Name: "echo", Arguments: `{}`},
		}},
	}, &types.TokenUsage{TotalTokens: 1}, nil
}

func (alwaysToolModel) Stream(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}

func (alwaysToolModel) CountTokens(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (int, error) {
	return 0, nil
}

func newTestRuntime(model kernel.Model) kernel.Runtime {
	echo := tool.NewSimpleFuncTool(
		"echo", "echo the arguments",
		map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		func(_ context.Context, args string) (string, error) { return args, nil },
	)
	return runtime.NewRuntime(model, tool.NewMapToolRegistry([]kernel.Tool{echo}))
}

func TestStepLoop_ReActLoop(t *testing.T) {
	model := &scriptedModel{}
	rt := newTestRuntime(model)
	sl := New()

	res := sl.Run(context.Background(), &coreengine.RunInput{
		AgentName: "a",
		Messages:  []*types.Message{types.NewUserMessage("hi")},
	}, rt)

	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if res.Reason != kernel.TerminateFinished {
		t.Fatalf("Reason = %v, want TerminateFinished", res.Reason)
	}
	if res.Content != "final answer" {
		t.Fatalf("Content = %q, want final answer", res.Content)
	}
	if model.calls != 2 {
		t.Fatalf("model called %d times, want 2 (tool call + final)", model.calls)
	}
	if len(res.Messages) != 4 {
		t.Fatalf("len(Messages) = %d, want 4 (user, assistant+toolcall, tool, assistant)", len(res.Messages))
	}
}

func TestStepLoop_MaxSteps(t *testing.T) {
	rt := newTestRuntime(alwaysToolModel{})
	sl := New(WithMaxSteps(2))

	res := sl.Run(context.Background(), &coreengine.RunInput{
		AgentName: "a",
		Messages:  []*types.Message{types.NewUserMessage("hi")},
	}, rt)

	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if res.Reason != kernel.TerminateMaxSteps {
		t.Fatalf("Reason = %v, want TerminateMaxSteps", res.Reason)
	}
}
