package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runner"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

func TestAgentContext(t *testing.T) {
	t.Run("NewAgentContext_creates_with_InvocationID_and_State", func(t *testing.T) {
		ac := runtime.NewAgentContext()
		if ac == nil {
			t.Fatal("NewAgentContext returned nil")
		}
		if ac.InvocationID() == "" {
			t.Fatal("InvocationID should not be empty")
		}
		if ac.State() == nil {
			t.Fatal("State should not be nil")
		}
		if ac.ContextPassing() != "full_dialogue" {
			t.Fatalf("ContextPassing = %q, want %q", ac.ContextPassing(), "full_dialogue")
		}
	})

	t.Run("WithContextState_sets_initial_state", func(t *testing.T) {
		sm := runtime.NewInMemoryState()
		sm.Set("key", "val")
		ac := runtime.NewAgentContext(runtime.WithContextState(sm))
		if v, _ := ac.State().Get("key"); v != "val" {
			t.Fatalf("State['key'] = %q, want %q", v, "val")
		}
	})

	t.Run("WithContextBranch_sets_branch", func(t *testing.T) {
		ac := runtime.NewAgentContext(runtime.WithContextBranch("feature/x"))
		if ac.Branch() != "feature/x" {
			t.Fatalf("Branch = %q, want %q", ac.Branch(), "feature/x")
		}
	})

	t.Run("WithContextPassing_sets_passing", func(t *testing.T) {
		ac := runtime.NewAgentContext(runtime.WithContextPassing("fresh_task"))
		if ac.ContextPassing() != "fresh_task" {
			t.Fatalf("ContextPassing = %q, want %q", ac.ContextPassing(), "fresh_task")
		}
	})

	t.Run("WithParentAgent_sets_parent", func(t *testing.T) {
		parent := &mockAgent{name: "parent"}
		ac := runtime.NewAgentContext(runtime.WithParentAgent(parent))
		if ac.ParentAgent() != parent {
			t.Fatal("ParentAgent not set")
		}
		// Verify dynamic type
		if _, ok := ac.ParentAgent().(*mockAgent); !ok {
			t.Fatal("ParentAgent is not *mockAgent")
		}
	})

	t.Run("WithRunPath_sets_path", func(t *testing.T) {
		ac := runtime.NewAgentContext(runtime.WithRunPath("root/child"))
		if ac.RunPath() != "root/child" {
			t.Fatalf("RunPath = %q, want %q", ac.RunPath(), "root/child")
		}
	})

	t.Run("WithAgentContext_and_GetAgentContext_roundtrip", func(t *testing.T) {
		original := runtime.NewAgentContext(runtime.WithContextBranch("test"))
		ctx := kernel.WithAgentContext(context.Background(), original)
		extracted := kernel.GetAgentContext(ctx)
		if extracted == nil {
			t.Fatal("GetAgentContext returned nil")
		}
		if extracted != original {
			t.Fatal("extracted context does not match original")
		}
	})

	t.Run("GetAgentContext_returns_nil_when_not_set", func(t *testing.T) {
		ac := kernel.GetAgentContext(context.Background())
		if ac != nil {
			t.Fatal("expected nil for context without AgentContext")
		}
	})

	t.Run("MustAgentContext_panics_when_not_set", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic from MustAgentContext")
			}
		}()
		kernel.MustAgentContext(context.Background())
	})

	t.Run("MustAgentContext_returns_when_set", func(t *testing.T) {
		original := runtime.NewAgentContext()
		ctx := kernel.WithAgentContext(context.Background(), original)
		ac := kernel.MustAgentContext(ctx)
		if ac != original {
			t.Fatal("MustAgentContext returned wrong context")
		}
	})

	t.Run("NewInvocationID_generates_unique_IDs", func(t *testing.T) {
		id1 := runtime.NewInvocationID()
		id2 := runtime.NewInvocationID()
		if id1 == "" {
			t.Fatal("InvocationID should not be empty")
		}
		if id1 == id2 {
			t.Fatal("InvocationIDs should be unique")
		}
	})
}

type mockAgent struct {
	name        string
	description string
	events      []*types.Event
}

type mockCheckpointStore struct {
	cps map[string]*types.Checkpoint
}

func (m *mockCheckpointStore) Save(_ context.Context, cp *types.Checkpoint) error {
	m.cps[cp.ID] = cp
	return nil
}

func (m *mockCheckpointStore) Load(_ context.Context, id string) (*types.Checkpoint, error) {
	cp, ok := m.cps[id]
	if !ok {
		return nil, errors.New("checkpoint not found")
	}
	return cp, nil
}

func (m *mockCheckpointStore) Delete(_ context.Context, id string) error {
	delete(m.cps, id)
	return nil
}

func (m *mockCheckpointStore) List(_ context.Context) ([]string, error) {
	ids := make([]string, 0, len(m.cps))
	for id := range m.cps {
		ids = append(ids, id)
	}
	return ids, nil
}

type mockModel struct{}

func (m *mockModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return types.NewAssistantMessage(""), &types.TokenUsage{}, nil
}

func (m *mockModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, nil
}

func (m *mockModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return 0, nil
}

func (a *mockAgent) Name() string                 { return a.name }
func (a *mockAgent) Description() string          { return a.description }
func (a *mockAgent) InputSchema() map[string]any  { return nil }
func (a *mockAgent) OutputSchema() map[string]any { return nil }
func (a *mockAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	var contentBuf strings.Builder
	var allMsgs []*types.Message
	if input != nil {
		allMsgs = make([]*types.Message, 0, len(input.Messages)+128)
		allMsgs = append(allMsgs, input.Messages...)
	}

	for _, evt := range a.events {
		switch evt.Type {
		case types.EventToken:
			contentBuf.WriteString(evt.Content)
		case types.EventToolResult:
			allMsgs = append(allMsgs, types.NewToolMessage(evt.Content, evt.ToolCallID, evt.ToolName))
		case types.EventFinish:
			if evt.Content != "" {
				contentBuf.Reset()
				contentBuf.WriteString(evt.Content)
			}
		case types.EventError:
			return &kernel.Result{
				Content:    contentBuf.String(),
				Messages:   allMsgs,
				TokenUsage: evt.Usage,
				Err:        evt.Error,
			}
		}
	}

	result := &kernel.Result{
		Content:  contentBuf.String(),
		Messages: allMsgs,
	}

	// Propagate TokenUsage from the last EventFinish
	for _, evt := range a.events {
		if evt.Type == types.EventFinish && evt.Usage != nil {
			result.TokenUsage = evt.Usage
		}
	}

	return result
}

func TestRunner(t *testing.T) {
	t.Run("NewRunner_creates_with_agent", func(t *testing.T) {
		agent := &mockAgent{name: "test-agent"}
		r := runner.NewRunner(agent, &mockModel{})
		if r == nil {
			t.Fatal("NewRunner returned nil")
		}
	})

	t.Run("Run_returns_AgentResult_on_EventFinish", func(t *testing.T) {
		agent := &mockAgent{
			name: "test",
			events: []*types.Event{
				{Type: types.EventToken, Content: "hello"},
				{Type: types.EventFinish, Content: "done", Usage: &types.TokenUsage{TotalTokens: 10}},
			},
		}
		r := runner.NewRunner(agent, &mockModel{})
		ctx := context.Background()
		input := &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}}
		info := r.Run(ctx, input)
		result, err := info.Result, info.Err
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if result == nil {
			t.Fatal("result is nil")
		}
		if result.Content != "done" {
			t.Fatalf("Content = %q, want %q", result.Content, "done")
		}
		if result.Err != nil {
			t.Fatal("Finished should be true")
		}
		if result.TokenUsage == nil {
			t.Fatal("TokenUsage should not be nil")
		}
		if result.TokenUsage.TotalTokens != 10 {
			t.Fatalf("TotalTokens = %d, want 10", result.TokenUsage.TotalTokens)
		}
	})

	t.Run("Run_propagates_EventError", func(t *testing.T) {
		expectedErr := errors.New("agent error")
		agent := &mockAgent{
			name:   "error-agent",
			events: []*types.Event{types.ErrorEvent(expectedErr)},
		}
		r := runner.NewRunner(agent, &mockModel{})
		info := r.Run(context.Background(), &types.AgentInput{})
		if info.Err != expectedErr {
			t.Fatalf("err = %v, want %v", info.Err, expectedErr)
		}
	})

	t.Run("Stream_returns_event_channel", func(t *testing.T) {
		agent := &mockAgent{
			name: "stream-test",
			events: []*types.Event{
				{Type: types.EventToken, Content: "tok1"},
				{Type: types.EventToken, Content: "tok2"},
				{Type: types.EventFinish},
			},
		}
		r := runner.NewRunner(agent, &mockModel{})
		it, err := r.Stream(context.Background(), &types.AgentInput{})
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		var received []*types.Event
		for evt := range it.Ch() {
			received = append(received, evt)
		}
		if len(received) != 1 {
			t.Fatalf("received %d events, want 1", len(received))
		}
		if received[0].Type != types.EventFinish {
			t.Fatalf("event type = %v, want EventFinish", received[0].Type)
		}
		if received[0].Content != "tok1tok2" {
			t.Fatalf("content = %q, want %q", received[0].Content, "tok1tok2")
		}
	})

	t.Run("Stream_propagates_EventError_as_channel_close", func(t *testing.T) {
		agent := &mockAgent{
			name:   "stream-err",
			events: []*types.Event{types.ErrorEvent(errors.New("stream error"))},
		}
		r := runner.NewRunner(agent, &mockModel{})
		it, err := r.Stream(context.Background(), &types.AgentInput{})
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		count := 0
		for range it.Ch() {
			count++
		}
		if count != 1 {
			t.Fatalf("received %d events before close, want 1", count)
		}
	})

	t.Run("Resume_requires_checkpoint_store", func(t *testing.T) {
		agent := &mockAgent{name: "test"}
		r := runner.NewRunner(agent, &mockModel{})
		_, err := r.Resume(context.Background(), "ckpt_1")
		if err == nil {
			t.Fatal("expected error when no checkpoint store")
		}
	})

	t.Run("Resume_with_checkpoint_store", func(t *testing.T) {
		agent := &mockAgent{
			name:   "resume-test",
			events: []*types.Event{{Type: types.EventFinish, Content: "resumed"}},
		}
		cs := &mockCheckpointStore{cps: make(map[string]*types.Checkpoint)}
		r := runner.NewRunner(agent, &mockModel{}, runner.WithRunnerCheckpointStore(cs))

		cp := &types.Checkpoint{
			ID: "ckpt_test",
		}
		cs.Save(context.Background(), cp)

		info, err := r.Resume(context.Background(), "ckpt_test")
		if err != nil {
			t.Fatalf("Resume error: %v", err)
		}
		if info == nil {
			t.Fatal("info is nil")
		}
		if info.Result == nil {
			t.Fatal("info.Result is nil")
		}
		if info.Result.Content != "resumed" {
			t.Fatalf("Content = %q, want %q", info.Result.Content, "resumed")
		}
	})
}

func TestCheckpoint(t *testing.T) {
	t.Run("CheckpointID_generates_unique_IDs", func(t *testing.T) {
		id1 := types.CheckpointID()
		id2 := types.CheckpointID()
		if id1 == "" {
			t.Fatal("CheckpointID should not be empty")
		}
		if id1 == id2 {
			t.Fatal("CheckpointIDs should be unique")
		}
	})
}

// flakyTool fails the first `fails` runs, then succeeds.
type flakyTool struct {
	name  string
	fails int32
	calls atomic.Int32
}

func (t *flakyTool) Name() string              { return t.name }
func (t *flakyTool) Description() string       { return "flaky test tool" }
func (t *flakyTool) Schema() map[string]any    { return map[string]any{"type": "object"} }
func (t *flakyTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Kind: kernel.ToolKindFunction} }
func (t *flakyTool) Run(ctx context.Context, argsJSON string) (string, error) {
	n := t.calls.Add(1)
	if n <= t.fails {
		return "", fmt.Errorf("boom attempt %d", n)
	}
	return fmt.Sprintf("ok attempt %d", n), nil
}

// TestExecToolsRetry covers tool-call retry behavior driven by RetryPolicy.
func TestExecToolsRetry(t *testing.T) {
	newRT := func(tl kernel.Tool, rp runtime.RetryPolicy) *runtime.Runtime {
		reg := tool.NewMapToolRegistry([]kernel.Tool{tl})
		return runtime.NewRuntime(&mockModel{}, reg, runtime.WithRetryPolicy(rp))
	}
	exec := func(rt *runtime.Runtime) *types.Message {
		msgs := rt.ExecTools(context.Background(), []*types.ToolCall{
			{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "flaky", Arguments: "{}"}},
		})
		if len(msgs) != 1 {
			t.Fatalf("got %d messages, want 1", len(msgs))
		}
		return msgs[0]
	}

	t.Run("retries failed tool when RetryOnTool", func(t *testing.T) {
		tl := &flakyTool{name: "flaky", fails: 1}
		rt := newRT(tl, runtime.RetryPolicy{RetryOnTool: true, MaxRetries: 2, BaseDelay: time.Millisecond})
		msg := exec(rt)
		if tl.calls.Load() != 2 {
			t.Fatalf("tool called %d times, want 2 (initial + 1 retry)", tl.calls.Load())
		}
		if !strings.Contains(msg.Content, "ok attempt 2") {
			t.Fatalf("content = %q, want successful retry result", msg.Content)
		}
	})

	t.Run("gives up after MaxRetries", func(t *testing.T) {
		tl := &flakyTool{name: "flaky", fails: 5}
		rt := newRT(tl, runtime.RetryPolicy{RetryOnTool: true, MaxRetries: 1, BaseDelay: time.Millisecond})
		msg := exec(rt)
		if tl.calls.Load() != 2 {
			t.Fatalf("tool called %d times, want 2 (initial + 1 retry)", tl.calls.Load())
		}
		if !strings.Contains(msg.Content, "boom attempt 2") {
			t.Fatalf("content = %q, want final failure after retries", msg.Content)
		}
	})

	t.Run("no retry when RetryOnTool disabled", func(t *testing.T) {
		tl := &flakyTool{name: "flaky", fails: 1}
		rt := newRT(tl, runtime.RetryPolicy{})
		msg := exec(rt)
		if tl.calls.Load() != 1 {
			t.Fatalf("tool called %d times, want 1 (no retry)", tl.calls.Load())
		}
		if !strings.Contains(msg.Content, "boom attempt 1") {
			t.Fatalf("content = %q, want original failure", msg.Content)
		}
	})
}

// ── RunStep lifecycle ─────────────────────────────────────────────────────

// noopBody 是 RunStep 测试用的一步编排：只记录被调用、原样透传 info。
func noopBody(called *bool) func(context.Context, *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
	return func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		*called = true
		return ctx, info, nil
	}
}

// TestRunStep_FiresStartAndEnd 验证 body 正常返回时 StepStart/StepEnd 各触发
// 一次且顺序为 start → body → end。
func TestRunStep_FiresStartAndEnd(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	var order []string
	rt.OnStepStart(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		order = append(order, "start")
		return ctx, info, nil
	})
	rt.OnStepEnd(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		order = append(order, "end")
		return ctx, info, nil
	})

	var bodyCalled bool
	info := &kernel.StepInfo{StepIndex: 0}
	ctx, cont, outInfo, err := rt.RunStep(context.Background(), info, noopBody(&bodyCalled))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !cont {
		t.Fatal("cont = false, want true")
	}
	if !bodyCalled {
		t.Fatal("body not called")
	}
	if len(order) != 2 || order[0] != "start" || order[1] != "end" {
		t.Fatalf("order = %v, want [start end]", order)
	}
	if outInfo == nil {
		t.Fatal("outInfo is nil")
	}
	if ctx == nil {
		t.Fatal("ctx is nil")
	}
}

// TestRunStep_StepStartAbortSkipsBodyAndEnd 验证 StepStart 置 Continue=false
// 时 body 与 StepEnd 都不执行。
func TestRunStep_StepStartAbortSkipsBodyAndEnd(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	rt.OnStepStart(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		info.Continue = false
		return ctx, info, nil
	})
	var endFired bool
	rt.OnStepEnd(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		endFired = true
		return ctx, info, nil
	})

	var bodyCalled bool
	info := &kernel.StepInfo{StepIndex: 0}
	_, cont, _, err := rt.RunStep(context.Background(), info, noopBody(&bodyCalled))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cont {
		t.Fatal("cont = true, want false")
	}
	if bodyCalled {
		t.Fatal("body called, want skipped")
	}
	if endFired {
		t.Fatal("StepEnd fired, want skipped")
	}
}

// TestRunStep_BodyErrorStillFiresEnd 验证 body 返回 error 时 StepEnd 仍触发，
// 且返回的 err 为 body 错误。
func TestRunStep_BodyErrorStillFiresEnd(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	var endFired bool
	rt.OnStepEnd(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		endFired = true
		return ctx, info, nil
	})

	bodyErr := errors.New("body boom")
	info := &kernel.StepInfo{StepIndex: 0}
	_, cont, _, err := rt.RunStep(context.Background(), info, func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		return ctx, info, bodyErr
	})
	if !endFired {
		t.Fatal("StepEnd not fired after body error")
	}
	if !errors.Is(err, bodyErr) {
		t.Fatalf("err = %v, want body error", err)
	}
	if !cont {
		t.Fatal("cont = false, want true (StepEnd did not stop)")
	}
}

// TestRunStep_BodyPanicStillFiresEnd 验证 body panic 时 StepEnd 触发后 panic
// 继续传播（引擎 bug 不吞）。
func TestRunStep_BodyPanicStillFiresEnd(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	var endFired bool
	rt.OnStepEnd(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		endFired = true
		return ctx, info, nil
	})

	info := &kernel.StepInfo{StepIndex: 0}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic from body")
			}
			if !endFired {
				t.Fatal("StepEnd not fired after body panic")
			}
		}()
		rt.RunStep(context.Background(), info, func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
			panic("body panic")
		})
	}()
}

// TestRunStep_EndContMergesIntoReturn 验证 StepEnd 置 Continue=false 时
// RunStep 返回 cont=false。
func TestRunStep_EndContMergesIntoReturn(t *testing.T) {
	rt := runtime.NewRuntime(&mockModel{}, nil)
	rt.OnStepEnd(func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		info.Continue = false
		return ctx, info, nil
	})

	info := &kernel.StepInfo{StepIndex: 0}
	_, cont, _, err := rt.RunStep(context.Background(), info, func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		return ctx, info, nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cont {
		t.Fatal("cont = true, want false (merged from StepEnd)")
	}
}
