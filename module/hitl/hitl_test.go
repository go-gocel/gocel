package hitl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

// ── mocks ───────────────────────────────────────────────────────────────

type mockRegistrar struct {
	toolCall func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error)
}

func (r *mockRegistrar) OnAgentStart(fn kernel.AgentStartHook) func()       { return nil }
func (r *mockRegistrar) OnAgentEnd(fn kernel.AgentEndHook) func()           { return nil }
func (r *mockRegistrar) OnMessagesBuilt(fn kernel.MessagesHook) func()      { return nil }
func (r *mockRegistrar) OnStepStart(fn kernel.StepHook) func()              { return nil }
func (r *mockRegistrar) OnStepEnd(fn kernel.StepHook) func()                { return nil }
func (r *mockRegistrar) OnModelCall(fn kernel.ModelCallHook) func()         { return nil }
func (r *mockRegistrar) OnModelResult(fn kernel.ModelResultHookFunc) func() { return nil }
func (r *mockRegistrar) OnToolCall(fn kernel.ToolCallHook) func() {
	r.toolCall = fn
	return func() {}
}
func (r *mockRegistrar) OnToolResult(fn kernel.ToolResultHookFunc) func() { return nil }
func (r *mockRegistrar) OnDecision(fn kernel.DecisionHook) func()         { return nil }

type fakeCtx struct {
	interrupt chan string
	sendEvent func(event *types.Event) bool
}

func (a *fakeCtx) InvocationID() string                    { return "test" }
func (a *fakeCtx) AgentName() string                       { return "" }
func (a *fakeCtx) Branch() string                          { return "" }
func (a *fakeCtx) RunPath() string                         { return "" }
func (a *fakeCtx) ContextPassing() string                  { return "" }
func (a *fakeCtx) EnableStreaming() bool                   { return false }
func (a *fakeCtx) InterruptInput() chan string             { return a.interrupt }
func (a *fakeCtx) SendEvent() func(*types.Event) bool      { return a.sendEvent }
func (a *fakeCtx) ParentAgent() kernel.Agent               { return nil }
func (a *fakeCtx) State() kernel.StateManager              { return nil }
func (a *fakeCtx) Facts() *types.RuntimeFacts              { return &types.RuntimeFacts{} }
func (a *fakeCtx) SetSendEvent(fn func(*types.Event) bool) {}
func (a *fakeCtx) SetInterruptInput(ch chan string)        {}

func newCtx(ch chan string, se func(*types.Event) bool) context.Context {
	if se == nil {
		se = func(*types.Event) bool { return true }
	}
	return kernel.WithAgentContext(context.Background(), &fakeCtx{interrupt: ch, sendEvent: se})
}

// ── Tool Approval ───────────────────────────────────────────────────────

func TestApprove_Reject(t *testing.T) {
	m := New(WithApproveTool("delete_file", types.HITLModeConfirm))
	r := &mockRegistrar{}
	m.Register(r)

	ch := make(chan string, 1)
	ch <- `{"approved":false,"feedback":"bad idea"}`

	_, _, err := r.toolCall(newCtx(ch, nil), &kernel.ToolCallInfo{Name: "delete_file", Args: `{"path":"/etc"}`})
	if err == nil {
		t.Fatal("expected rejection")
	}
	if err.Error() != "bad idea" {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestApprove_PassWhenNoMatch(t *testing.T) {
	m := New(WithApproveTool("delete_file", types.HITLModeConfirm))
	r := &mockRegistrar{}
	m.Register(r)

	_, out, err := r.toolCall(context.Background(), &kernel.ToolCallInfo{Name: "echo", Args: "{}"})
	if err != nil {
		t.Fatalf("unrelated tool should pass: %v", err)
	}
	if out.Name != "echo" {
		t.Fatal("passthrough failed")
	}
}

func TestApprove_ModifiedArgs(t *testing.T) {
	m := New(WithApproveTool("send_msg", types.HITLModeReview))
	r := &mockRegistrar{}
	m.Register(r)

	ch := make(chan string, 1)
	b, _ := json.Marshal(map[string]any{
		"approved":      true,
		"modified_args": `{"msg":"corrected"}`,
	})
	ch <- string(b)

	_, out, err := r.toolCall(newCtx(ch, nil), &kernel.ToolCallInfo{Name: "send_msg", Args: `{"msg":"original"}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Args != `{"msg":"corrected"}` {
		t.Fatalf("args = %q", out.Args)
	}
}

func TestApprove_Timeout(t *testing.T) {
	m := New(WithApproveTool("slow", types.HITLModeConfirm), WithTimeout(50*time.Millisecond))
	r := &mockRegistrar{}
	m.Register(r)

	// Nobody responds → default behavior rejects (no approval means no execution).
	_, _, err := r.toolCall(newCtx(make(chan string), nil), &kernel.ToolCallInfo{Name: "slow", Args: "{}"})
	if err == nil {
		t.Fatal("default timeout behavior should reject, not pass through")
	}
	if !strings.Contains(err.Error(), "timeout rejected") {
		t.Fatalf("error = %q, want timeout-rejected message", err.Error())
	}
}

func TestApprove_TimeoutBehaviorSkip(t *testing.T) {
	m := New(WithApproveTool("slow", types.HITLModeConfirm),
		WithTimeout(50*time.Millisecond), WithTimeoutBehavior(TimeoutSkip))
	r := &mockRegistrar{}
	m.Register(r)

	_, out, err := r.toolCall(newCtx(make(chan string), nil), &kernel.ToolCallInfo{Name: "slow", Args: "{}"})
	if err != nil {
		t.Fatalf("TimeoutSkip should not error: %v", err)
	}
	if out == nil {
		t.Fatal("TimeoutSkip should pass through")
	}
}

// ── Decision audit ───────────────────────────────────────────────────────

// decisionCtx builds an AgentContext + Runtime, capturing decision hooks.
func decisionCtx(ch chan string) (context.Context, func() *kernel.DecisionInfo, func() int) {
	rt := runtime.NewRuntime(nil, nil)
	var (
		got   *kernel.DecisionInfo
		count int
	)
	rt.OnDecision(func(ctx context.Context, info *kernel.DecisionInfo) (context.Context, *kernel.DecisionInfo, error) {
		got = info
		count++
		return ctx, info, nil
	})
	ctx := kernel.WithRuntime(newCtx(ch, nil), rt)
	return ctx, func() *kernel.DecisionInfo { return got }, func() int { return count }
}

// TestDecision_Approved 验证批准时上报 approved 决策。
func TestDecision_Approved(t *testing.T) {
	m := New(WithApproveTool("delete_file", types.HITLModeConfirm))
	r := &mockRegistrar{}
	m.Register(r)

	ch := make(chan string, 1)
	ch <- `{"approved":true}`
	ctx, got, _ := decisionCtx(ch)

	if _, _, err := r.toolCall(ctx, &kernel.ToolCallInfo{Name: "delete_file", Args: `{"path":"/tmp/x"}`}); err != nil {
		t.Fatalf("approval should pass: %v", err)
	}
	if d := got(); d == nil {
		t.Fatal("decision hook not fired")
	} else {
		if d.Decision != "approved" || d.Tool != "delete_file" {
			t.Fatalf("decision = %+v, want approved/delete_file", d)
		}
	}
}

// TestDecision_Rejected 验证拒绝时上报 rejected 决策与理由。
func TestDecision_Rejected(t *testing.T) {
	m := New(WithApproveTool("delete_file", types.HITLModeConfirm))
	r := &mockRegistrar{}
	m.Register(r)

	ch := make(chan string, 1)
	ch <- `{"approved":false,"feedback":"bad idea"}`
	ctx, got, _ := decisionCtx(ch)

	if _, _, err := r.toolCall(ctx, &kernel.ToolCallInfo{Name: "delete_file", Args: `{"path":"/etc"}`}); err == nil {
		t.Fatal("rejection should error")
	}
	if d := got(); d == nil {
		t.Fatal("decision hook not fired")
	} else {
		if d.Decision != "rejected" || d.Reason != "bad idea" {
			t.Fatalf("decision = %+v, want rejected/bad idea", d)
		}
	}
}

// TestDecision_TimeoutRejected 验证超时拒绝上报 timeout_rejected 决策。
func TestDecision_TimeoutRejected(t *testing.T) {
	m := New(WithApproveTool("slow", types.HITLModeConfirm), WithTimeout(50*time.Millisecond))
	r := &mockRegistrar{}
	m.Register(r)

	ctx, got, _ := decisionCtx(make(chan string))
	if _, _, err := r.toolCall(ctx, &kernel.ToolCallInfo{Name: "slow", Args: "{}"}); err == nil {
		t.Fatal("timeout should reject by default")
	}
	if d := got(); d == nil {
		t.Fatal("decision hook not fired")
	} else {
		if d.Decision != "timeout_rejected" {
			t.Fatalf("decision = %q, want timeout_rejected", d.Decision)
		}
	}
}

// TestDecision_NoDecisionOnPassThrough 验证无匹配规则时不产生决策事件。
func TestDecision_NoDecisionOnPassThrough(t *testing.T) {
	m := New(WithApproveTool("delete_file", types.HITLModeConfirm))
	r := &mockRegistrar{}
	m.Register(r)

	ctx, _, count := decisionCtx(make(chan string))
	if _, _, err := r.toolCall(ctx, &kernel.ToolCallInfo{Name: "echo", Args: "{}"}); err != nil {
		t.Fatalf("unrelated tool should pass: %v", err)
	}
	if count() != 0 {
		t.Fatalf("fired %d decisions for passthrough tool, want 0", count())
	}
}

func TestApprove_TimeoutBehaviorApprove(t *testing.T) {
	m := New(WithApproveTool("slow", types.HITLModeConfirm),
		WithTimeout(50*time.Millisecond), WithTimeoutBehavior(TimeoutApprove))
	r := &mockRegistrar{}
	m.Register(r)

	_, out, err := r.toolCall(newCtx(make(chan string), nil), &kernel.ToolCallInfo{Name: "slow", Args: "{}"})
	if err != nil {
		t.Fatalf("TimeoutApprove should not error: %v", err)
	}
	if out == nil {
		t.Fatal("TimeoutApprove should pass through")
	}
}

func TestApprove_TimeoutBehaviorError(t *testing.T) {
	m := New(WithApproveTool("slow", types.HITLModeConfirm),
		WithTimeout(50*time.Millisecond), WithTimeoutBehavior(TimeoutError))
	r := &mockRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(newCtx(make(chan string), nil), &kernel.ToolCallInfo{Name: "slow", Args: "{}"})
	if err == nil {
		t.Fatal("TimeoutError should return an error")
	}
	if !strings.Contains(err.Error(), "timeout waiting for human response") {
		t.Fatalf("error = %q", err.Error())
	}
}

// ── Conditional Approval ────────────────────────────────────────────────

func TestConditional_InterceptWhenTrue(t *testing.T) {
	m := New(WithConditionalApprove("rm", types.HITLModeConfirm, func(args string) bool {
		return !strings.Contains(args, "/tmp") // intercept non-tmp paths
	}))
	r := &mockRegistrar{}
	m.Register(r)

	// Deleting /etc → should intercept
	ch := make(chan string, 1)
	ch <- `{"approved":false,"feedback":"protected path"}`
	_, _, err := r.toolCall(newCtx(ch, nil), &kernel.ToolCallInfo{Name: "rm", Args: `{"path":"/etc/passwd"}`})
	if err == nil {
		t.Fatal("expected interception for /etc")
	}
}

func TestConditional_PassWhenFalse(t *testing.T) {
	m := New(WithConditionalApprove("rm", types.HITLModeConfirm, func(args string) bool {
		return !strings.Contains(args, "/tmp")
	}))
	r := &mockRegistrar{}
	m.Register(r)

	// Deleting /tmp → should NOT intercept
	_, out, err := r.toolCall(context.Background(), &kernel.ToolCallInfo{Name: "rm", Args: `{"path":"/tmp/test.txt"}`})
	if err != nil {
		t.Fatalf("should pass through for /tmp: %v", err)
	}
	if out.Name != "rm" {
		t.Fatal("passthrough failed")
	}
}

func TestConditional_MixedRules(t *testing.T) {
	callCount := 0
	m := New(
		WithApproveTool("always_check", types.HITLModeConfirm),
		WithConditionalApprove("sometimes_check", types.HITLModeConfirm, func(args string) bool {
			callCount++
			return strings.Contains(args, "important")
		}),
	)
	r := &mockRegistrar{}
	m.Register(r)

	// always_check → always intercepts
	ch := make(chan string, 1)
	ch <- `{"approved":true}`
	_, _, err := r.toolCall(newCtx(ch, nil), &kernel.ToolCallInfo{Name: "always_check", Args: "{}"})
	if err != nil {
		t.Fatalf("always_check should be approved: %v", err)
	}

	// sometimes_check with non-important arg → pass through
	_, _, err = r.toolCall(context.Background(), &kernel.ToolCallInfo{Name: "sometimes_check", Args: `{"type":"trivial"}`})
	if err != nil {
		t.Fatalf("trivial should pass through: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("condition called %d times, want 1", callCount)
	}

	// sometimes_check with important arg → intercept
	ch2 := make(chan string, 1)
	ch2 <- `{"approved":false,"feedback":"no"}`
	_, _, err = r.toolCall(newCtx(ch2, nil), &kernel.ToolCallInfo{Name: "sometimes_check", Args: `{"type":"important"}`})
	if err == nil {
		t.Fatal("important should be intercepted")
	}
	if callCount != 2 {
		t.Fatalf("condition called %d times, want 2", callCount)
	}
}

// ── Dialogue ────────────────────────────────────────────────────────────

func TestDialogue_AsTools(t *testing.T) {
	m := New(WithUserDialogue("ask_user", "Ask the user"))
	tools := m.AsTools()
	if len(tools) != 1 || tools[0].Name() != "ask_user" {
		t.Fatalf("expected ask_user tool")
	}
}

func TestDialogue_PlainText(t *testing.T) {
	m := New(WithUserDialogue("ask_user", "Ask user"))
	ch := make(chan string, 1)
	ch <- "JSON please"

	dt := &dialogueTool{name: "ask_user", desc: "", mod: m}
	result, err := dt.Run(newCtx(ch, nil), `{"question":"What format?"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "JSON please" {
		t.Fatalf("result = %q", result)
	}
}

func TestDialogue_SelectedOption(t *testing.T) {
	m := New(WithUserDialogue("ask_user", "Ask user"))
	ch := make(chan string, 1)
	ch <- `{"selected_option":"yaml","approved":true}`

	dt := &dialogueTool{name: "ask_user", desc: "", mod: m}
	result, err := dt.Run(newCtx(ch, nil), `{"question":"Pick format","options":["json","yaml","xml"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "yaml") {
		t.Fatalf("result should contain selected option: %q", result)
	}
}

// TestDialogue_BatchQuestions: a batch of questions is asked in one
// interruption and the answers are rendered per question (DSH
// user-questions batch).
func TestDialogue_BatchQuestions(t *testing.T) {
	m := New(WithUserDialogue("ask_user", "Ask user"))
	ch := make(chan string, 1)
	ch <- `{"answers":[{"id":"lang","selected":["go"]},{"id":"ci","custom":"github actions"}]}`

	dt := &dialogueTool{name: "ask_user", desc: "", mod: m}
	result, err := dt.Run(newCtx(ch, nil), `{"questions":[
		{"id":"lang","question":"Language?","options":["go","rust"]},
		{"id":"ci","question":"CI system?"}
	]}`)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if !strings.Contains(result, "Language?: go") || !strings.Contains(result, "CI system?: github actions") {
		t.Fatalf("batch result = %q, want both answers rendered", result)
	}
}

// TestDialogue_IntentRidesTheInterrupt: the presentation intent (e.g.
// plan-review) travels on the interrupt event for UIs that recognise it.
func TestDialogue_IntentRidesTheInterrupt(t *testing.T) {
	m := New(WithUserDialogue("ask_user", "Ask user"))
	ch := make(chan string, 1)
	ch <- `{"answers":[{"id":"p","selected":["Approve"]}]}`

	var got *types.HITLInfo
	dt := &dialogueTool{name: "ask_user", desc: "", mod: m}
	if _, err := dt.Run(newCtx(ch, func(ev *types.Event) bool {
		if ev.Type == types.EventInterrupt && ev.Meta != nil {
			if hi, ok := ev.Meta["hitl_info"].(*types.HITLInfo); ok {
				got = hi
			}
		}
		return true
	}), `{"questions":[{"id":"p","question":"Approve the plan?"}],"intent":"plan-review"}`); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got == nil || got.Intent != "plan-review" {
		t.Fatalf("intent = %+v, want plan-review on the interrupt", got)
	}
	if len(got.Questions) != 1 || got.Questions[0].ID != "p" || got.Questions[0].Question != "Approve the plan?" {
		t.Fatalf("batch questions on interrupt = %+v", got.Questions)
	}
}

func TestDialogue_MultiTurn(t *testing.T) {
	// Agent can call ask_user multiple times in one execution
	m := New(WithUserDialogue("ask_user", "Ask user"))
	ch := make(chan string, 2)
	ch <- "JSON"
	ch <- "是"

	dt := &dialogueTool{name: "ask_user", desc: "", mod: m}

	// Turn 1
	r1, err := dt.Run(newCtx(ch, nil), `{"question":"请选择格式"}`)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if r1 != "JSON" {
		t.Fatalf("turn 1 = %q", r1)
	}

	// Turn 2: agent follows up based on human's answer
	r2, err := dt.Run(newCtx(ch, nil), `{"question":"确认用JSON格式？"}`)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if r2 != "是" {
		t.Fatalf("turn 2 = %q", r2)
	}
}

func TestDialogue_SendsInterruptEvent(t *testing.T) {
	m := New(WithUserDialogue("ask_user", "Ask user"))
	ch := make(chan string, 1)
	ch <- "ok"

	var sentType types.EventType
	dt := &dialogueTool{name: "ask_user", desc: "", mod: m}
	_, err := dt.Run(newCtx(ch, func(evt *types.Event) bool {
		sentType = evt.Type
		if info, ok := evt.Meta["hitl_info"].(*types.HITLInfo); ok {
			if info.Prompt == "" {
				t.Error("hitl info should have prompt")
			}
		}
		return true
	}), `{"question":"Proceed?","options":["a","b"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sentType != types.EventInterrupt {
		t.Fatalf("event type = %v, want EventInterrupt", sentType)
	}
}

func TestDialogue_ToolNotBlockedByApproval(t *testing.T) {
	m := New(
		WithApproveTool("delete_file", types.HITLModeConfirm),
		WithUserDialogue("ask_user", "Ask user"),
	)
	r := &mockRegistrar{}
	m.Register(r)

	// Dialogue tool passes through approval hook
	_, out, err := r.toolCall(context.Background(), &kernel.ToolCallInfo{Name: "ask_user", Args: `{"question":"test"}`})
	if err != nil {
		t.Fatalf("dialogue blocked by approval: %v", err)
	}
	if out.Name != "ask_user" {
		t.Fatal("dialogue should pass through")
	}
}

func TestDialogue_AllThreeModesTogether(t *testing.T) {
	m := New(
		WithApproveTool("delete_file", types.HITLModeConfirm),
		WithConditionalApprove("rm", types.HITLModeConfirm, func(args string) bool {
			return strings.Contains(args, "important")
		}),
		WithUserDialogue("ask_user", "Ask user"),
	)
	r := &mockRegistrar{}
	m.Register(r)

	ch := make(chan string, 3)

	// 1. approve tool
	ch <- `{"approved":true}`
	_, _, err := r.toolCall(newCtx(ch, nil), &kernel.ToolCallInfo{Name: "delete_file", Args: "{}"})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	// 2. conditional tool with non-matching arg
	_, _, err = r.toolCall(context.Background(), &kernel.ToolCallInfo{Name: "rm", Args: `trivial`})
	if err != nil {
		t.Fatalf("conditional pass: %v", err)
	}

	// 3. dialogue tool
	ch <- "你好"
	dt := &dialogueTool{name: "ask_user", desc: "", mod: m}
	r3, err := dt.Run(newCtx(ch, nil), `{"question":"有问题吗？"}`)
	if err != nil {
		t.Fatalf("dialogue: %v", err)
	}
	if r3 != "你好" {
		t.Fatalf("dialogue = %q", r3)
	}
}
