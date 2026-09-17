package runtime_test

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// simpleTool is a no-op tool for correlation tests.
type simpleTool struct{}

func (t *simpleTool) Name() string           { return "noop" }
func (t *simpleTool) Description() string    { return "noop test tool" }
func (t *simpleTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *simpleTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{Kind: kernel.ToolKindFunction}
}
func (t *simpleTool) Run(ctx context.Context, argsJSON string) (string, error) {
	return "ok", nil
}

// newCorrRuntime builds a runtime and a context carrying an AgentContext with
// the given name and a step index (as FireStepStart would inject).
func newCorrRuntime(agentName string, step int) (*runtime.Runtime, context.Context) {
	rt := runtime.NewRuntime(&mockModel{}, tool.NewMapToolRegistry([]kernel.Tool{&simpleTool{}}))
	ac := runtime.NewAgentContext(runtime.WithContextAgentName(agentName))
	ctx := kernel.WithAgentContext(context.Background(), ac)
	ctx = kernel.WithStepIndex(ctx, step)
	return rt, ctx
}

// TestFireStepStart_InjectsInvocationIDAndStepIndex verifies that
// FireStepStart enriches StepInfo with the invocation id and injects the
// step index into the returned context.
func TestFireStepStart_InjectsInvocationIDAndStepIndex(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	ac := runtime.NewAgentContext(runtime.WithContextAgentName("agent-a"))
	ctx := kernel.WithAgentContext(context.Background(), ac)

	var sawInvocationID string
	rt.OnStepStart(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		sawInvocationID = info.InvocationID
		return ctx, info, nil
	})

	ctx2, cont, info, err := rt.FireStepStart(ctx, &kernel.StepInfo{AgentName: "agent-a", StepIndex: 3})
	if err != nil {
		t.Fatalf("FireStepStart: %v", err)
	}
	if !cont {
		t.Fatal("continue = false, want true")
	}
	if sawInvocationID != ac.InvocationID() {
		t.Fatalf("hook saw InvocationID %q, want %q", sawInvocationID, ac.InvocationID())
	}
	if info.InvocationID != ac.InvocationID() {
		t.Fatalf("info.InvocationID = %q, want %q", info.InvocationID, ac.InvocationID())
	}
	if n, ok := kernel.StepIndexFromContext(ctx2); !ok || n != 3 {
		t.Fatalf("step index in returned ctx = %d (ok=%v), want 3", n, ok)
	}
}

// TestFireStepStart_InjectsStepIndexWithoutHooks verifies the step cursor is
// injected even when no step hooks are registered — model/tool hooks rely on it.
func TestFireStepStart_InjectsStepIndexWithoutHooks(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	ctx := kernel.WithAgentContext(context.Background(), runtime.NewAgentContext())
	ctx2, _, _, err := rt.FireStepStart(ctx, &kernel.StepInfo{StepIndex: 7})
	if err != nil {
		t.Fatalf("FireStepStart: %v", err)
	}
	if n, ok := kernel.StepIndexFromContext(ctx2); !ok || n != 7 {
		t.Fatalf("step index = %d (ok=%v), want 7", n, ok)
	}
}

// TestFireStepStart_SurvivesHookContextReplacement verifies the step index is
// re-injected on the returned context even when a hook swaps the context.
func TestFireStepStart_SurvivesHookContextReplacement(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	rt.OnStepStart(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		type freshKey struct{}
		return context.WithValue(ctx, freshKey{}, "x"), info, nil
	})

	ctx2, _, _, err := rt.FireStepStart(context.Background(), &kernel.StepInfo{StepIndex: 9})
	if err != nil {
		t.Fatalf("FireStepStart: %v", err)
	}
	if n, ok := kernel.StepIndexFromContext(ctx2); !ok || n != 9 {
		t.Fatalf("step index = %d (ok=%v), want 9", n, ok)
	}
}

// TestModelHooks_CarryCorrelation verifies model hooks see the full
// InvocationID/AgentName/StepIndex correlation on their info.
func TestModelHooks_CarryCorrelation(t *testing.T) {
	rt, ctx := newCorrRuntime("agent-a", 2)

	var callInfo, resultInfo *kernel.ModelCallInfo
	rt.OnModelCall(func(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
		callInfo = info
		return ctx, info, nil
	})
	rt.OnModelResult(func(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
		resultInfo = info
		return ctx, info, nil
	})

	if _, _, err := rt.CallModel(ctx, []*types.Message{types.NewUserMessage("hi")}); err != nil {
		t.Fatalf("CallModel: %v", err)
	}

	check := func(name string, info *kernel.ModelCallInfo) {
		t.Helper()
		if info == nil {
			t.Fatalf("%s: info is nil", name)
		}
		if info.InvocationID == "" {
			t.Fatalf("%s: InvocationID is empty", name)
		}
		if info.AgentName != "agent-a" {
			t.Fatalf("%s: AgentName = %q, want agent-a", name, info.AgentName)
		}
		if info.StepIndex != 2 {
			t.Fatalf("%s: StepIndex = %d, want 2", name, info.StepIndex)
		}
	}
	check("OnModelCall", callInfo)
	check("OnModelResult", resultInfo)
}

// TestToolHooks_CarryCorrelation verifies tool hooks see the full correlation.
func TestToolHooks_CarryCorrelation(t *testing.T) {
	rt, ctx := newCorrRuntime("agent-b", 4)

	var callInfo, resultInfo *kernel.ToolCallInfo
	rt.OnToolCall(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		callInfo = info
		return ctx, info, nil
	})
	rt.OnToolResult(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		resultInfo = info
		return ctx, info, nil
	})

	msgs := rt.ExecTools(ctx, []*types.ToolCall{
		{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "noop", Arguments: "{}"}},
	})
	if len(msgs) != 1 {
		t.Fatalf("ExecTools returned %d messages, want 1", len(msgs))
	}

	check := func(name string, info *kernel.ToolCallInfo) {
		t.Helper()
		if info == nil {
			t.Fatalf("%s: info is nil", name)
		}
		if info.InvocationID == "" {
			t.Fatalf("%s: InvocationID is empty", name)
		}
		if info.AgentName != "agent-b" {
			t.Fatalf("%s: AgentName = %q, want agent-b", name, info.AgentName)
		}
		if info.StepIndex != 4 {
			t.Fatalf("%s: StepIndex = %d, want 4", name, info.StepIndex)
		}
	}
	check("OnToolCall", callInfo)
	check("OnToolResult", resultInfo)
}

// TestModelHooks_WithoutStep_HaveStepIndexMinusOne verifies calls outside a
// step loop carry StepIndex -1 (unknown) instead of a bogus step number.
func TestModelHooks_WithoutStep_HaveStepIndexMinusOne(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	ac := runtime.NewAgentContext(runtime.WithContextAgentName("agent-c"))
	ctx := kernel.WithAgentContext(context.Background(), ac)

	var info *kernel.ModelCallInfo
	rt.OnModelCall(func(ctx context.Context, i *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
		info = i
		return ctx, i, nil
	})

	if _, _, err := rt.CallModel(ctx, []*types.Message{types.NewUserMessage("hi")}); err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	if info == nil {
		t.Fatal("info is nil")
	}
	if info.StepIndex != -1 {
		t.Fatalf("StepIndex = %d, want -1", info.StepIndex)
	}
	if info.InvocationID != ac.InvocationID() {
		t.Fatalf("InvocationID = %q, want %q", info.InvocationID, ac.InvocationID())
	}
	if info.AgentName != "agent-c" {
		t.Fatalf("AgentName = %q, want agent-c", info.AgentName)
	}
}

// TestFireDecision_EnrichesAndFires verifies decision hooks receive the info
// enriched with run/step identity and the hook can see the decision fields.
func TestFireDecision_EnrichesAndFires(t *testing.T) {
	rt, ctx := newCorrRuntime("agent-d", 5)

	var saw *kernel.DecisionInfo
	rt.OnDecision(func(ctx context.Context, info *kernel.DecisionInfo) (context.Context, *kernel.DecisionInfo, error) {
		saw = info
		return ctx, info, nil
	})

	err := rt.FireDecision(ctx, &kernel.DecisionInfo{
		Tool:     "terminal",
		Args:     "rm -rf /",
		Decision: "hard_blocked",
		Reason:   "dangerous",
	})
	if err != nil {
		t.Fatalf("FireDecision: %v", err)
	}
	if saw == nil {
		t.Fatal("decision hook not fired")
	}
	if saw.InvocationID == "" {
		t.Fatal("InvocationID is empty")
	}
	if saw.AgentName != "agent-d" {
		t.Fatalf("AgentName = %q, want agent-d", saw.AgentName)
	}
	if saw.StepIndex != 5 {
		t.Fatalf("StepIndex = %d, want 5", saw.StepIndex)
	}
	if saw.Tool != "terminal" || saw.Decision != "hard_blocked" || saw.Reason != "dangerous" {
		t.Fatalf("decision fields lost: %+v", saw)
	}
}

// TestFireDecision_NoHooksIsNoop verifies FireDecision without hooks and
// without a runtime is safe.
func TestFireDecision_NoHooksIsNoop(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	if err := rt.FireDecision(context.Background(), &kernel.DecisionInfo{}); err != nil {
		t.Fatalf("FireDecision: %v", err)
	}
	var nilRT *runtime.Runtime
	if err := nilRT.FireDecision(context.Background(), &kernel.DecisionInfo{}); err != nil {
		t.Fatalf("FireDecision(nil): %v", err)
	}
}
