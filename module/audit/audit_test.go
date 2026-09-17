package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// mockRegistrar captures the hooks registered by the module.
type mockRegistrar struct {
	modelCall   func(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error)
	modelResult func(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error)
	toolCall    func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error)
	toolResult  func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error)
	agentEnd    func(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error)
	decision    func(ctx context.Context, info *kernel.DecisionInfo) (context.Context, *kernel.DecisionInfo, error)
}

func (r *mockRegistrar) OnAgentStart(fn kernel.AgentStartHook) func() { return func() {} }
func (r *mockRegistrar) OnAgentEnd(fn kernel.AgentEndHook) func() {
	r.agentEnd = fn
	return func() {}
}
func (r *mockRegistrar) OnMessagesBuilt(fn kernel.MessagesHook) func() { return func() {} }
func (r *mockRegistrar) OnStepStart(fn kernel.StepHook) func()         { return func() {} }
func (r *mockRegistrar) OnStepEnd(fn kernel.StepHook) func()           { return func() {} }
func (r *mockRegistrar) OnModelCall(fn kernel.ModelCallHook) func() {
	r.modelCall = fn
	return func() {}
}
func (r *mockRegistrar) OnModelResult(fn kernel.ModelResultHookFunc) func() {
	r.modelResult = fn
	return func() {}
}
func (r *mockRegistrar) OnToolCall(fn kernel.ToolCallHook) func() {
	r.toolCall = fn
	return func() {}
}
func (r *mockRegistrar) OnToolResult(fn kernel.ToolResultHookFunc) func() {
	r.toolResult = fn
	return func() {}
}
func (r *mockRegistrar) OnDecision(fn kernel.DecisionHook) func() {
	r.decision = fn
	return func() {}
}

// newTextModule builds a module writing human-readable lines into a buffer.
func newTextModule() (*Module, *bytes.Buffer) {
	var buf bytes.Buffer
	m := New(WithLogger(log.New(&buf, "", 0)))
	return m, &buf
}

// newJSONModule builds a module writing JSON lines into a buffer.
func newJSONModule(opts ...Option) (*Module, *bytes.Buffer) {
	var buf bytes.Buffer
	m := New(append([]Option{WithWriter(&buf)}, opts...)...)
	return m, &buf
}

// jsonLines parses every JSON line in the buffer into Events.
func jsonLines(t *testing.T, buf *bytes.Buffer) []Event {
	t.Helper()
	raw := strings.TrimSpace(buf.String())
	if raw == "" {
		return nil
	}
	var evs []Event
	for _, line := range strings.Split(raw, "\n") {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestNew_DefaultLogger(t *testing.T) {
	// 无选项时回退默认 stderr 文本 logger，不 panic。
	m := New()
	if m == nil || m.cfg.logger == nil {
		t.Fatal("New() should install the default logger")
	}
	if m.cfg.sampleRate != 1 {
		t.Fatalf("sampleRate = %v, want 1", m.cfg.sampleRate)
	}
}

func TestNew_WriterOnlySkipsDefaultLogger(t *testing.T) {
	m := New(WithWriter(&bytes.Buffer{}))
	if m.cfg.writer == nil {
		t.Fatal("writer not set")
	}
	if m.cfg.logger != nil {
		t.Fatal("writer-only module should not install the default logger")
	}
}

func TestRegister_RegistersAllHooks(t *testing.T) {
	m, _ := newTextModule()
	r := &mockRegistrar{}
	m.Register(r)

	if r.modelCall == nil || r.modelResult == nil {
		t.Fatal("model hooks not registered")
	}
	if r.toolCall == nil || r.toolResult == nil {
		t.Fatal("tool hooks not registered")
	}
	if r.agentEnd == nil {
		t.Fatal("agentEnd hook not registered")
	}
	if r.decision == nil {
		t.Fatal("decision hook not registered")
	}
}

func TestModelCallAudit(t *testing.T) {
	m, buf := newTextModule()
	r := &mockRegistrar{}
	m.Register(r)
	ctx := context.Background()

	// Call: logs message count and estimated tokens.
	msgs := []*types.Message{types.NewUserMessage("hello")}
	if _, _, err := r.modelCall(ctx, &kernel.ModelCallInfo{Messages: msgs}); err != nil {
		t.Fatalf("modelCall: %v", err)
	}
	if !strings.Contains(buf.String(), "MODEL START 1 messages") {
		t.Fatalf("missing MODEL START entry, got: %s", buf.String())
	}

	// Result (success): logs actual token usage.
	info := &kernel.ModelCallInfo{
		Messages: msgs,
		Usage:    &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	if _, _, err := r.modelResult(ctx, info); err != nil {
		t.Fatalf("modelResult: %v", err)
	}
	if !strings.Contains(buf.String(), "MODEL DONE  prompt=10 completion=5 total=15") {
		t.Fatalf("missing MODEL DONE entry, got: %s", buf.String())
	}

	// Result (error): logs MODEL ERROR.
	buf.Reset()
	errInfo := &kernel.ModelCallInfo{Error: context.DeadlineExceeded}
	if _, _, err := r.modelResult(ctx, errInfo); err != nil {
		t.Fatalf("modelResult(err): %v", err)
	}
	if !strings.Contains(buf.String(), "MODEL ERROR") {
		t.Fatalf("missing MODEL ERROR entry, got: %s", buf.String())
	}
}

func TestToolCallAudit(t *testing.T) {
	m, buf := newTextModule()
	r := &mockRegistrar{}
	m.Register(r)
	ctx := context.Background()

	if _, _, err := r.toolCall(ctx, &kernel.ToolCallInfo{Name: "calc"}); err != nil {
		t.Fatalf("toolCall: %v", err)
	}
	if !strings.Contains(buf.String(), "TOOL START  calc") {
		t.Fatalf("missing TOOL START entry, got: %s", buf.String())
	}

	buf.Reset()
	okInfo := &kernel.ToolCallInfo{Name: "calc", Result: "42"}
	if _, _, err := r.toolResult(ctx, okInfo); err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	if !strings.Contains(buf.String(), "TOOL DONE   calc (2 chars)") {
		t.Fatalf("missing TOOL DONE entry, got: %s", buf.String())
	}

	buf.Reset()
	errInfo := &kernel.ToolCallInfo{Name: "calc", Error: context.DeadlineExceeded}
	if _, _, err := r.toolResult(ctx, errInfo); err != nil {
		t.Fatalf("toolResult(err): %v", err)
	}
	if !strings.Contains(buf.String(), "TOOL ERROR  calc") {
		t.Fatalf("missing TOOL ERROR entry, got: %s", buf.String())
	}
}

// TestToolResult_ErrorPayload 验证 gocel 约定：工具失败以 {"error": ...}
// 作为结果内容时，审计记录归类为 error。
func TestToolResult_ErrorPayload(t *testing.T) {
	m, buf := newJSONModule()
	r := &mockRegistrar{}
	m.Register(r)

	_, _, err := r.toolResult(context.Background(), &kernel.ToolCallInfo{
		Name:   "terminal",
		Result: `{"error":"command blocked"}`,
	})
	if err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	evs := jsonLines(t, buf)
	if len(evs) != 1 {
		t.Fatalf("got %d records, want 1", len(evs))
	}
	if evs[0].Status != "error" {
		t.Fatalf("status = %q, want error", evs[0].Status)
	}
	if evs[0].Error != `{"error":"command blocked"}` {
		t.Fatalf("error = %q", evs[0].Error)
	}
}

func TestRunAudit(t *testing.T) {
	m, buf := newTextModule()
	r := &mockRegistrar{}
	m.Register(r)
	ctx := context.Background()

	// Success run.
	okInfo := &kernel.RunInfo{
		InvocationID: "inv_1",
		AgentName:    "agent-a",
		AllMsgs:      []*types.Message{types.NewUserMessage("hi"), types.NewAssistantMessage("hello")},
		Result:       &kernel.Result{Content: "hello", TokenUsage: &types.TokenUsage{TotalTokens: 30}},
	}
	if _, _, err := r.agentEnd(ctx, okInfo); err != nil {
		t.Fatalf("agentEnd: %v", err)
	}
	if !strings.Contains(buf.String(), "RUN DONE  agent=agent-a msgs=2 tokens=30") {
		t.Fatalf("missing RUN DONE entry, got: %s", buf.String())
	}

	// Failed run.
	buf.Reset()
	errInfo := &kernel.RunInfo{AgentName: "agent-b", Err: context.Canceled}
	if _, _, err := r.agentEnd(ctx, errInfo); err != nil {
		t.Fatalf("agentEnd(err): %v", err)
	}
	if !strings.Contains(buf.String(), "RUN ERROR agent=agent-b") {
		t.Fatalf("missing RUN ERROR entry, got: %s", buf.String())
	}
}

// TestWithWriter_JSONLinesAndCorrelation 验证结构化输出与关联字段落盘。
func TestWithWriter_JSONLinesAndCorrelation(t *testing.T) {
	m, buf := newJSONModule()
	r := &mockRegistrar{}
	m.Register(r)
	ctx := context.Background()

	if _, _, err := r.toolCall(ctx, &kernel.ToolCallInfo{
		Name: "calc", Args: `{"expr":"1+1"}`,
		InvocationID: "inv_1", AgentName: "agent-a", StepIndex: 2,
	}); err != nil {
		t.Fatalf("toolCall: %v", err)
	}
	if _, _, err := r.toolResult(ctx, &kernel.ToolCallInfo{
		Name: "calc", Args: `{"expr":"1+1"}`, Result: "42",
		InvocationID: "inv_1", AgentName: "agent-a", StepIndex: 2,
	}); err != nil {
		t.Fatalf("toolResult: %v", err)
	}

	evs := jsonLines(t, buf)
	if len(evs) != 2 {
		t.Fatalf("got %d records, want 2", len(evs))
	}
	for i, ev := range evs {
		if ev.Kind != "tool" {
			t.Fatalf("record %d kind = %q, want tool", i, ev.Kind)
		}
		if ev.InvocationID != "inv_1" || ev.AgentName != "agent-a" || ev.StepIndex != 2 {
			t.Fatalf("record %d correlation lost: %+v", i, ev)
		}
		if ev.Time.IsZero() {
			t.Fatalf("record %d time is zero", i)
		}
	}
}

func TestDecisionAudit(t *testing.T) {
	m, buf := newJSONModule()
	r := &mockRegistrar{}
	m.Register(r)
	if r.decision == nil {
		t.Fatal("decision hook not registered")
	}

	_, _, err := r.decision(context.Background(), &kernel.DecisionInfo{
		Tool: "terminal", Args: "rm -rf /",
		Decision:     "hard_blocked",
		Reason:       "dangerous",
		InvocationID: "inv_1", AgentName: "agent-a", StepIndex: 3,
	})
	if err != nil {
		t.Fatalf("decision: %v", err)
	}

	evs := jsonLines(t, buf)
	if len(evs) != 1 {
		t.Fatalf("got %d records, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Kind != "decision" {
		t.Fatalf("kind = %q, want decision", ev.Kind)
	}
	if ev.Decision != "hard_blocked" || ev.Reason != "dangerous" || ev.Tool != "terminal" {
		t.Fatalf("decision fields lost: %+v", ev)
	}
	if ev.InvocationID != "inv_1" || ev.AgentName != "agent-a" || ev.StepIndex != 3 {
		t.Fatalf("correlation lost: %+v", ev)
	}
}

// TestNilDecisionInfo 验证 nil 决策 info 不 panic、不产出记录。
func TestNilDecisionInfo(t *testing.T) {
	m, buf := newJSONModule()
	r := &mockRegistrar{}
	m.Register(r)

	if _, _, err := r.decision(context.Background(), nil); err != nil {
		t.Fatalf("decision(nil): %v", err)
	}
	if evs := jsonLines(t, buf); len(evs) != 0 {
		t.Fatalf("got %d records for nil decision, want 0", len(evs))
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 3); got != "hel…" {
		t.Fatalf("truncate(hello,3) = %q, want hel…", got)
	}
	if got := truncate("hi", 3); got != "hi" {
		t.Fatalf("truncate(hi,3) = %q, want hi", got)
	}
	if got := truncate("hello", 0); got != "hello" {
		t.Fatalf("truncate(hello,0) = %q, want hello (no truncation)", got)
	}
	if got := truncate("你好世界", 3); got != "你好世…" {
		t.Fatalf("truncate(你好世界,3) = %q, want 你好世…", got)
	}
}

// TestTruncationApplied 验证截断上限落到 JSON 记录。
func TestTruncationApplied(t *testing.T) {
	m, buf := newJSONModule(WithMaxArgsLen(5), WithMaxResultLen(10))
	r := &mockRegistrar{}
	m.Register(r)

	_, _, err := r.toolResult(context.Background(), &kernel.ToolCallInfo{
		Name:   "calc",
		Args:   "a very long argument string",
		Result: "a very long result string that exceeds the cap",
	})
	if err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	evs := jsonLines(t, buf)
	if len(evs) != 1 {
		t.Fatalf("got %d records, want 1", len(evs))
	}
	if evs[0].Args != "a ver…" {
		t.Fatalf("args = %q, want truncated to 5 runes", evs[0].Args)
	}
	if evs[0].Result != "a very lon…" {
		t.Fatalf("result = %q, want truncated to 10 runes", evs[0].Result)
	}
}

// TestSampleRate 验证采样率：0.5 每两个记一个；0 全不记。
func TestSampleRate(t *testing.T) {
	ctx := context.Background()
	info := &kernel.ToolCallInfo{Name: "calc", StepIndex: -1}

	// rate 0.5 → 4 个事件记 2 个（确定性：第 1、3 个）。
	m, buf := newJSONModule(WithSampleRate(0.5))
	r := &mockRegistrar{}
	m.Register(r)
	for i := 0; i < 4; i++ {
		if _, _, err := r.toolCall(ctx, info); err != nil {
			t.Fatalf("toolCall: %v", err)
		}
	}
	if evs := jsonLines(t, buf); len(evs) != 2 {
		t.Fatalf("rate 0.5 over 4 events: got %d records, want 2", len(evs))
	}

	// rate 0 → 不记任何事件。
	m2, buf2 := newJSONModule(WithSampleRate(0))
	r2 := &mockRegistrar{}
	m2.Register(r2)
	if _, _, err := r2.toolCall(ctx, info); err != nil {
		t.Fatalf("toolCall: %v", err)
	}
	if evs := jsonLines(t, buf2); len(evs) != 0 {
		t.Fatalf("rate 0: got %d records, want 0", len(evs))
	}
}
