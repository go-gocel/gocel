package tool_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

type testAgentForTool struct {
	name        string
	description string
	tokenEvents []string
	errEvent    error
	mu          sync.Mutex
}

func (a *testAgentForTool) Name() string                 { return a.name }
func (a *testAgentForTool) Description() string          { return a.description }
func (a *testAgentForTool) InputSchema() map[string]any  { return nil }
func (a *testAgentForTool) OutputSchema() map[string]any { return nil }
func (a *testAgentForTool) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	var content string
	for _, tok := range a.tokenEvents {
		content += tok
	}
	if a.errEvent != nil {
		return &kernel.Result{Content: content, Err: a.errEvent}
	}
	return &kernel.Result{Content: content}
}

func TestNewAgentTool(t *testing.T) {
	agent := &testAgentForTool{name: "sub-agent", description: "a sub agent"}
	tool := tool.NewAgentTool("my_tool", "my tool description", agent)
	if tool == nil {
		t.Fatal("NewAgentTool returned nil")
	}
	if tool.Name() != "my_tool" {
		t.Fatalf("Name() = %q, want %q", tool.Name(), "my_tool")
	}
	if tool.Description() != "my tool description" {
		t.Fatalf("Description() = %q, want %q", tool.Description(), "my tool description")
	}
	if tool.Schema() == nil {
		t.Fatal("Schema() should not be nil")
	}
}

func newAgentTestCtx() context.Context {
	return kernel.WithRuntime(context.Background(), runtime.NewRuntime(nil, nil))
}

func TestAgentTool_Run(t *testing.T) {
	t.Run("returns_accumulated_text", func(t *testing.T) {
		agent := &testAgentForTool{
			name:        "calc",
			description: "calculator",
			tokenEvents: []string{"The ", "result ", "is 42"},
		}
		tool := tool.NewAgentTool("calc_tool", "calculator tool", agent)
		result, err := tool.Run(newAgentTestCtx(), `{"expr": "2+2"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "The result is 42" {
			t.Fatalf("got %q, want %q", result, "The result is 42")
		}
	})

	t.Run("returns_error_from_agent", func(t *testing.T) {
		expectedErr := errors.New("agent error")
		agent := &testAgentForTool{
			name:        "failing",
			description: "failing agent",
			tokenEvents: []string{"partial "},
			errEvent:    expectedErr,
		}
		tool := tool.NewAgentTool("fail_tool", "failing tool", agent)
		result, err := tool.Run(newAgentTestCtx(), `{}`)
		if err != expectedErr {
			t.Fatalf("err = %v, want %v", err, expectedErr)
		}
		if result != "partial " {
			t.Fatalf("result = %q, want %q", result, "partial ")
		}
	})

	t.Run("single_token", func(t *testing.T) {
		agent := &testAgentForTool{
			name:        "echo",
			description: "echo",
			tokenEvents: []string{"hello"},
		}
		tool := tool.NewAgentTool("echo_tool", "echo tool", agent)
		result, err := tool.Run(newAgentTestCtx(), `"world"`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello" {
			t.Fatalf("got %q, want %q", result, "hello")
		}
	})

	t.Run("no_tokens", func(t *testing.T) {
		agent := &testAgentForTool{
			name:        "silent",
			description: "silent agent",
		}
		tool := tool.NewAgentTool("silent_tool", "silent tool", agent)
		result, err := tool.Run(newAgentTestCtx(), `{}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Fatalf("got %q, want empty", result)
		}
	})
}

func TestMustAgentTool(t *testing.T) {
	agent := &testAgentForTool{name: "sub", description: "sub agent"}
	tool := tool.MustAgentTool(agent)
	if tool == nil {
		t.Fatal("MustAgentTool returned nil")
	}
	if tool.Name() != "sub" {
		t.Fatalf("Name() = %q, want %q", tool.Name(), "sub")
	}
	if tool.Description() != "sub agent" {
		t.Fatalf("Description() = %q, want %q", tool.Description(), "sub agent")
	}
}
