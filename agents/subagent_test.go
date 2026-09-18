package agents

import (
	"context"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	coreruntime "github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/runner"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/tools/subagent"
)

func TestNewSubAgent_Defaults(t *testing.T) {
	a := NewSubAgent()
	if a.Name() != "subagent" {
		t.Fatalf("Name = %q, want subagent", a.Name())
	}

	r := runner.NewRunner(a, echoModel{})
	info := r.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	if info.Err != nil {
		t.Fatalf("Run: %v", info.Err)
	}
	if info.Result == nil || info.Result.Content != "done" {
		t.Fatalf("result = %+v, want content done", info.Result)
	}
}

func TestNewSubAgent_Overrides(t *testing.T) {
	a := NewSubAgent(WithName("scout"), WithDescription("explores"), WithMaxSteps(3))
	if a.Name() != "scout" {
		t.Fatalf("Name = %q, want scout", a.Name())
	}
	if a.Description() != "explores" {
		t.Fatalf("Description = %q, want explores", a.Description())
	}
}

func TestNewDelegation_Tools(t *testing.T) {
	d := NewDelegation()
	tools, err := d.Tools()
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	names := make(map[string]bool, len(tools))
	for _, tl := range tools {
		names[tl.Name()] = true
	}
	for _, want := range []string{"subagent", "subagent_fork", "send_message", "interrupt_agent", "list_agents"} {
		if !names[want] {
			t.Errorf("delegation tools missing %q (got %v)", want, names)
		}
	}
}

func TestNewDelegation_DefaultFactory(t *testing.T) {
	d := NewDelegation()
	child := d.Agent(context.Background())
	if child == nil {
		t.Fatal("Agent returned nil")
	}
	if child.Name() != "subagent" {
		t.Fatalf("child Name = %q, want subagent", child.Name())
	}
}

func TestNewDelegation_CustomFactory(t *testing.T) {
	d := NewDelegation(WithSubAgentFactory(func(context.Context) kernel.Agent {
		return New(WithName("custom-child"))
	}))
	if got := d.Agent(context.Background()).Name(); got != "custom-child" {
		t.Fatalf("child Name = %q, want custom-child", got)
	}
}

// TestDelegation_SpawnThroughTool 走完整链路：父运行时 + 委派工具 → 子代理进入注册表。
func TestDelegation_SpawnThroughTool(t *testing.T) {
	d := NewDelegation(WithSubAgentFactory(func(context.Context) kernel.Agent {
		return New(WithName("child"), WithSystemPrompt("be brief"))
	}))
	tools, err := d.Tools()
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	rt := coreruntime.NewRuntime(echoModel{}, nil)
	ctx := kernel.WithRuntime(context.Background(), rt)
	ctx = kernel.WithAgentContext(ctx, coreruntime.NewAgentContext(coreruntime.WithContextAgentName("parent")))

	spawn := findTool(t, tools, "subagent")
	out, err := spawn.Run(ctx, `{"task":"算一下 1+1","label":"math"}`)
	if err != nil {
		t.Fatalf("subagent tool: %v", err)
	}
	if !strings.Contains(out, `"id":"sub-`) {
		t.Fatalf("handle = %s, want a subagent id", out)
	}

	live := d.Registry().List(ctx)
	if len(live) != 1 {
		t.Fatalf("live subagents = %d, want 1", len(live))
	}
	if live[0].Label != "math" {
		t.Errorf("label = %q, want math", live[0].Label)
	}
	if err := d.Registry().Dispose(context.Background(), live[0].ID); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
}

// TestDelegation_MaxDepthRejects 验证深度上限确实被工具执行。
func TestDelegation_MaxDepthRejects(t *testing.T) {
	d := NewDelegation(WithDelegationDepth(1))
	tools, err := d.Tools()
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	rt := coreruntime.NewRuntime(echoModel{}, nil)
	ctx := kernel.WithRuntime(context.Background(), rt)
	ctx = kernel.WithAgentContext(ctx, coreruntime.NewAgentContext(coreruntime.WithContextAgentName("parent")))
	// 当前已处于第 1 层，深度上限 1 → 再派发应被拒。
	ctx = subagent.WithDepth(ctx, 1)

	spawn := findTool(t, tools, "subagent")
	if _, err := spawn.Run(ctx, `{"task":"再派一个"}`); err == nil {
		t.Fatal("spawn beyond depth limit succeeded, want rejection")
	} else if !strings.Contains(err.Error(), "depth") {
		t.Fatalf("error = %v, want a depth-limit error", err)
	}
}

func findTool(t *testing.T, tools []kernel.Tool, name string) kernel.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}
