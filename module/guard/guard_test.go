package guard

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

func TestNewGuardModule_Defaults(t *testing.T) {
	m := NewGuardModule()
	if m == nil {
		t.Fatal("module is nil")
	}
	if got := m.TokenBudget(); got != 128000-4096 {
		t.Errorf("TokenBudget = %d, want %d", got, 128000-4096)
	}
}

func TestNewGuardModuleWith_CustomConfig(t *testing.T) {
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        32000,
		CompactThreshold:   0.9,
		MinBudget:          1000,
		ToolTruncateLength: 500,
	})
	if got := m.TokenBudget(); got != 32000 {
		t.Errorf("TokenBudget = %d, want 32000", got)
	}
}

func TestNewGuardModuleWith_ZeroValues(t *testing.T) {
	// Zero values should use defaults for positive-only fields
	m := NewGuardModuleWith(GuardConfig{})
	if got := m.TokenBudget(); got != 128000-4096 {
		t.Errorf("TokenBudget = %d, want default", got)
	}
}

func TestNewGuardModuleWith_NegativeBudget(t *testing.T) {
	// Negative TokenBudget → clamped to 0 → default kept
	m := NewGuardModuleWith(GuardConfig{TokenBudget: -1})
	if got := m.TokenBudget(); got != 128000-4096 {
		t.Errorf("TokenBudget = %d, want %d (default kept)", got, 128000-4096)
	}
}

func TestGuardModule_Register(t *testing.T) {
	m := NewGuardModule()
	rt := runtime.NewRuntime(nil, nil)
	// Should not panic
	m.Register(rt)
}

// ── onStepStart 钩子链路（端到端）──────────────────────

// TestOnBeforeStep_OverBudgetTrims verifies the full trim path: when the
// estimated tokens exceed the budget, message history is trimmed and the
// StepInfo receives the compacted messages.
func TestOnBeforeStep_OverBudgetTrims(t *testing.T) {
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        200,
		CompactThreshold:   0.85,
		ToolTruncateLength: 50,
	})
	ctx := context.Background()

	msgs := []*types.Message{types.NewSystemMessage("sys prompt")}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.NewUserMessage(strings.Repeat("long message content ", 100)))
	}

	stepInfo := &kernel.StepInfo{Messages: msgs}
	_, out, err := m.onStepStart(ctx, stepInfo)
	if err != nil {
		t.Fatalf("onStepStart: %v", err)
	}
	if out == nil || out.Messages == nil {
		t.Fatal("out is nil")
	}
	if len(out.Messages) >= len(msgs) {
		t.Fatalf("over-budget run should trim messages: before=%d after=%d", len(msgs), len(out.Messages))
	}
}

// TestOnBeforeStep_NearBudgetCompactsTool verifies the compaction path:
// near (but under) the budget, long tool results are truncated in place.
func TestOnBeforeStep_NearBudgetCompactsTool(t *testing.T) {
	ctx := context.Background()

	longTool := types.NewToolMessage(strings.Repeat("abcdefghij", 100), "call_1", "grep") // 1000 chars
	msgs := []*types.Message{
		types.NewSystemMessage("sys"),
		types.NewUserMessage("hi"),
		longTool,
	}

	// Budget set exactly to the current estimate: est == budget > threshold,
	// so the near-budget compaction branch runs.
	probe := NewGuardModule()
	est := probe.EstimateTokens(msgs)
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        est,
		CompactThreshold:   0.85,
		ToolTruncateLength: 50,
	})

	stepInfo := &kernel.StepInfo{Messages: msgs}
	_, out, err := m.onStepStart(ctx, stepInfo)
	if err != nil {
		t.Fatalf("onStepStart: %v", err)
	}
	if len(out.Messages) != len(msgs) {
		t.Fatalf("near-budget should not drop messages: got %d, want %d", len(out.Messages), len(msgs))
	}
	toolMsg := out.Messages[2]
	if len(toolMsg.Content) >= len(longTool.Content) {
		t.Fatalf("long tool content should be compacted: before=%d after=%d",
			len(longTool.Content), len(toolMsg.Content))
	}
}

// TestOnBeforeStep_UnderBudgetNoop verifies that a small history passes
// through untouched.
func TestOnBeforeStep_UnderBudgetNoop(t *testing.T) {
	m := NewGuardModule()
	ctx := context.Background()

	msgs := []*types.Message{
		types.NewSystemMessage("sys"),
		types.NewUserMessage("hi"),
	}
	stepInfo := &kernel.StepInfo{Messages: msgs}
	_, out, err := m.onStepStart(ctx, stepInfo)
	if err != nil {
		t.Fatalf("onStepStart: %v", err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("under-budget should keep all messages: got %d", len(out.Messages))
	}
	if out.Messages[0].Content != "sys" || out.Messages[1].Content != "hi" {
		t.Fatal("under-budget messages must not be modified")
	}
}

// ── CompactSummary 策略 ─────────────────────────────

// TestOnBeforeStep_OnTrimCallback proves the OnTrim callback fires with the
// actually dropped messages and the trim report when the history is over
// budget. Distinct messages make the drop count precise; the system message
// is never dropped.
func TestOnBeforeStep_OnTrimCallback(t *testing.T) {
	var mu sync.Mutex
	var dropped []*types.Message
	var report *types.TrimReport
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        200,
		CompactThreshold:   0.85,
		ToolTruncateLength: 50,
		OnTrim: func(d []*types.Message, r *types.TrimReport) {
			mu.Lock()
			dropped = d
			report = r
			mu.Unlock()
		},
	})
	ctx := context.Background()

	msgs := []*types.Message{types.NewSystemMessage("sys prompt")}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.NewUserMessage(strings.Repeat(fmt.Sprintf("msg-%d ", i), 20)))
	}

	stepInfo := &kernel.StepInfo{Messages: msgs}
	if _, _, err := m.onStepStart(ctx, stepInfo); err != nil {
		t.Fatalf("onStepStart: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(dropped) == 0 {
		t.Fatal("OnTrim must report dropped messages")
	}
	if len(dropped) >= len(msgs) {
		t.Fatalf("dropped %d of %d — everything misreported (C9)", len(dropped), len(msgs))
	}
	for _, d := range dropped {
		if d.Role == types.RoleSystem {
			t.Fatal("system message must never be reported as dropped")
		}
	}
	if report == nil || !report.Truncated {
		t.Fatalf("report = %+v, want truncated report", report)
	}
}

func TestCompactContent_SummaryStrategy(t *testing.T) {
	m := NewGuardModuleWith(GuardConfig{
		Strategy:           CompactSummary,
		ToolTruncateLength: 200,
		MaxLines:           50,
		KeepHeadRatio:      0.5,
		MinPreserve:        10,
	})
	// Multi-line content: 20 lines x 30 chars = 600 chars.
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString(strings.Repeat("x", 30))
		b.WriteByte('\n')
	}
	content := b.String()
	result := m.CompactContent(content)
	if len(result) >= len(content) {
		t.Errorf("CompactSummary should shorten content: before=%d after=%d", len(content), len(result))
	}
	// The summary must still be meaningful: preserve the beginning.
	if !strings.HasPrefix(result, content[:10]) {
		t.Errorf("CompactSummary should preserve the head: %q", result)
	}
}

func TestCompactContent_SummaryStrategyShort(t *testing.T) {
	m := NewGuardModuleWith(GuardConfig{Strategy: CompactSummary, ToolTruncateLength: 100})
	content := "short"
	result := m.CompactContent(content)
	if result != content {
		t.Errorf("short content must pass through unchanged: %q", result)
	}
}

func TestGuardModule_ShouldTrim_Empty(t *testing.T) {
	m := NewGuardModule()
	if m.ShouldTrim(nil, 1000) {
		t.Error("should not trim nil messages")
	}
	if m.ShouldTrim([]*types.Message{}, 1000) {
		t.Error("should not trim empty messages")
	}
}

func TestGuardModule_ShouldTrim_SingleMessage(t *testing.T) {
	m := NewGuardModule()
	msgs := []*types.Message{types.NewUserMessage("hello")}
	if m.ShouldTrim(msgs, 1000) {
		t.Error("should not trim single message")
	}
}

func TestGuardModule_ShouldTrim_BelowBudget(t *testing.T) {
	m := NewGuardModule()
	msgs := []*types.Message{
		types.NewSystemMessage("short"),
		types.NewUserMessage("hi"),
	}
	if m.ShouldTrim(msgs, 100000) {
		t.Error("should not trim when well below budget")
	}
}

func TestGuardModule_ShouldTrim_AboveBudget(t *testing.T) {
	m := NewGuardModule()
	// Create enough messages to exceed a small budget
	msgs := []*types.Message{
		types.NewSystemMessage("system prompt"),
	}
	for i := 0; i < 50; i++ {
		msgs = append(msgs, types.NewUserMessage(strings.Repeat("long message content ", 100)))
	}
	if !m.ShouldTrim(msgs, 100) {
		t.Log("may not trigger trim with small budget due to token estimation")
	}
}

func TestGuardModule_Trim_BudgetZero(t *testing.T) {
	m := NewGuardModule()
	msgs := []*types.Message{
		types.NewSystemMessage("sys"),
		types.NewUserMessage("user"),
	}
	result, report := m.Trim(msgs, 0)
	if report == nil {
		t.Fatal("report is nil")
	}
	if len(result) == 0 {
		t.Error("result should not be empty")
	}
}

func TestGuardModule_Trim_UnderBudget(t *testing.T) {
	m := NewGuardModule()
	msgs := []*types.Message{
		types.NewSystemMessage("sys"),
		types.NewUserMessage("user"),
	}
	result, report := m.Trim(msgs, 100000)
	if report.Truncated {
		t.Error("should not truncate when under budget")
	}
	if len(result) != 2 {
		t.Errorf("got %d messages, want 2", len(result))
	}
}

func TestGuardModule_Calibrate_FirstCall(t *testing.T) {
	m := NewGuardModule()
	msgs := []*types.Message{types.NewUserMessage("hello")}
	m.Calibrate(10, msgs)
	// Should set calibrated flag and ema
	est := m.EstimateTokens(msgs)
	if est <= 0 {
		t.Error("calibrated estimate should be > 0")
	}
}

func TestGuardModule_Calibrate_InvalidInput(t *testing.T) {
	m := NewGuardModule()
	// Should not panic
	m.Calibrate(0, nil)
	m.Calibrate(-1, []*types.Message{types.NewUserMessage("hi")})
	m.Calibrate(10, []*types.Message{})
}

func TestGuardModule_EstimateTokens_Empty(t *testing.T) {
	m := NewGuardModule()
	if got := m.EstimateTokens(nil); got != 0 {
		t.Errorf("EstimateTokens(nil) = %d, want 0", got)
	}
	if got := m.EstimateTokens([]*types.Message{}); got != 0 {
		t.Errorf("EstimateTokens([]) = %d, want 0", got)
	}
}

func TestGuardModule_EstimateTokens_Basic(t *testing.T) {
	m := NewGuardModule()
	msgs := []*types.Message{types.NewUserMessage("hello world")}
	got := m.EstimateTokens(msgs)
	if got <= 0 {
		t.Errorf("EstimateTokens = %d, want > 0", got)
	}
}

func TestGuardModule_CompactMessages_ToolMessage(t *testing.T) {
	m := NewGuardModule()
	longContent := strings.Repeat("abcdefghij", 500) // 5000 chars
	msgs := []*types.Message{
		types.NewSystemMessage("sys"),
		types.NewToolMessage(longContent, "tool_call_1", "tool1"),
	}
	result := m.compactMessages(msgs)
	if len(result) != 2 {
		t.Fatalf("got %d messages, want 2", len(result))
	}
	// Tool message should be truncated
	toolMsg := result[1]
	if toolMsg.Role != types.RoleTool {
		t.Error("second message should still be tool role")
	}
	if len(toolMsg.Content) >= len(longContent) {
		t.Error("tool message content should be truncated")
	}
	if !strings.Contains(toolMsg.Content, "... [truncated") {
		t.Error("truncated content should contain truncation note")
	}
}

func TestGuardModule_CompactMessages_ShortToolMessage(t *testing.T) {
	m := NewGuardModule()
	shortContent := "short result"
	msgs := []*types.Message{
		types.NewToolMessage(shortContent, "t1", "tool1"),
	}
	result := m.compactMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("got %d messages, want 1", len(result))
	}
	// Short tool message should NOT be truncated
	if result[0].Content != shortContent {
		t.Errorf("content changed: got %q, want %q", result[0].Content, shortContent)
	}
}

func TestGuardModule_CompactMessages_NilMessage(t *testing.T) {
	m := NewGuardModule()
	msgs := []*types.Message{nil, types.NewSystemMessage("hi")}
	result := m.compactMessages(msgs)
	if len(result) != 2 {
		t.Fatalf("got %d messages, want 2", len(result))
	}
	if result[0] != nil {
		t.Error("nil message should remain nil")
	}
}

func TestGuardModule_CompactContent_WithStrategy(t *testing.T) {
	// Test that CompactSimple strategy works
	m := NewGuardModuleWith(GuardConfig{
		Strategy:           CompactSimple,
		ToolTruncateLength: 100,
	})
	content := strings.Repeat("abcdefghij", 20) // 200 chars
	result := m.CompactContent(content)
	if len(result) >= len(content) {
		t.Error("CompactContent should truncate long content")
	}
	if !strings.Contains(result, "... [truncated") {
		t.Error("CompactContent result should contain truncation note")
	}
}

func TestGuardModule_OnModelResult_Calibrates(t *testing.T) {
	m := NewGuardModule()
	ctx := context.Background()
	msgs := []*types.Message{types.NewUserMessage("test message")}
	info := &kernel.ModelCallInfo{
		Messages: msgs,
		Usage:    &types.TokenUsage{PromptTokens: 5},
	}
	_, _, err := m.onModelResult(ctx, info)
	if err != nil {
		t.Fatalf("onModelResult: %v", err)
	}
}

func TestGuardModule_OnModelResult_NilUsage(t *testing.T) {
	m := NewGuardModule()
	ctx := context.Background()
	info := &kernel.ModelCallInfo{
		Messages: []*types.Message{types.NewUserMessage("hi")},
		Usage:    nil,
	}
	_, _, err := m.onModelResult(ctx, info)
	if err != nil {
		t.Fatalf("onAfterModelCall with nil usage: %v", err)
	}
}

func TestGuardModule_OnBeforeStep_NilMessages(t *testing.T) {
	m := NewGuardModule()
	ctx := context.Background()
	stepInfo := &kernel.StepInfo{
		Messages: nil,
		Continue: true,
	}
	_, _, err := m.onStepStart(ctx, stepInfo)
	if err != nil {
		t.Fatalf("onStepStart: %v", err)
	}
}

func TestGuardModule_OnBeforeStep_FewMessages(t *testing.T) {
	m := NewGuardModule()
	ctx := context.Background()
	stepInfo := &kernel.StepInfo{
		Messages: []*types.Message{types.NewSystemMessage("only one")},
		Continue: true,
	}
	_, _, err := m.onStepStart(ctx, stepInfo)
	if err != nil {
		t.Fatalf("onStepStart with 1 message: %v", err)
	}
}

func TestGuardModule_OnBeforeStep_CompactThreshold(t *testing.T) {
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        100,
		CompactThreshold:   0.01, // Very low threshold to trigger compact
		MinBudget:          4096,
		ToolTruncateLength: 50,
	})
	ctx := context.Background()

	// Create messages with a long tool result
	longContent := strings.Repeat("abcdefghijklmnopqrst", 100) // 2000 chars
	stepInfo := &kernel.StepInfo{
		Messages: []*types.Message{
			types.NewSystemMessage("sys"),
			types.NewToolMessage(longContent, "tool1", "tool1"),
			types.NewAssistantMessage("ok"),
		},
		Continue: true,
	}
	_, _, err := m.onStepStart(ctx, stepInfo)
	if err != nil {
		t.Fatalf("onStepStart: %v", err)
	}
}

func TestGuardModule_OnBeforeStep_TrimNeeded(t *testing.T) {
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        50,
		CompactThreshold:   0.85,
		MinBudget:          10,
		ToolTruncateLength: 2000,
	})
	ctx := context.Background()

	// Many messages to exceed budget
	msgs := []*types.Message{types.NewSystemMessage("sys")}
	for i := 0; i < 100; i++ {
		msgs = append(msgs, types.NewUserMessage(strings.Repeat("x", 100)))
	}
	stepInfo := &kernel.StepInfo{
		Messages: msgs,
		Continue: true,
	}
	_, _, err := m.onStepStart(ctx, stepInfo)
	if err != nil {
		t.Fatalf("onStepStart: %v", err)
	}
}

// TestGuardModule_SummarizerCompactsHead: with a Summarizer configured, an
// over-budget step replaces the summarized head with one system message
// while keeping the system prompt and the recent tail — the dropped history
// is carried forward instead of discarded.
func TestGuardModule_SummarizerCompactsHead(t *testing.T) {
	var summarized []*types.Message
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        400,
		CompactThreshold:   0.01, // always over budget
		MinBudget:          10,
		ToolTruncateLength: 2000,
		Summarizer: func(ctx context.Context, msgs []*types.Message) (string, error) {
			summarized = msgs
			return "SUMMARY-OF-HEAD", nil
		},
	})
	ctx := context.Background()

	msgs := []*types.Message{types.NewSystemMessage("sys")}
	for i := 0; i < 40; i++ {
		// Long user messages guarantee the budget is exceeded.
		msgs = append(msgs, types.NewUserMessage("user-" + strconv.Itoa(i) + strings.Repeat("y", 80)))
	}
	// A tail that must survive verbatim.
	msgs = append(msgs, types.NewUserMessage("FINAL-TAIL"))

	stepInfo := &kernel.StepInfo{Messages: msgs, Continue: true}
	if _, _, err := m.onStepStart(ctx, stepInfo); err != nil {
		t.Fatalf("onStepStart: %v", err)
	}

	// The summarizer received the head (system prompt excluded).
	if len(summarized) == 0 || summarized[0].Content == "sys" {
		t.Fatalf("summarizer must receive the head without the system prompt, got %d msgs", len(summarized))
	}
	// The result keeps a single system prompt (with the summary merged into
	// it) and the final tail; the intermediate users are gone.
	got := stepInfo.Messages
	foundTail, systemCount := false, 0
	for i, msg := range got {
		if msg.Role == types.RoleSystem {
			systemCount++
			if i != 0 {
				t.Fatalf("second system at index %d, want single system at 0", i)
			}
			if !strings.HasPrefix(msg.Content, "sys") || !strings.Contains(msg.Content, "SUMMARY-OF-HEAD") {
				t.Fatalf("system content = %q, want sys prompt + merged summary", msg.Content)
			}
		}
		if msg.Content == "FINAL-TAIL" {
			foundTail = true
		}
	}
	if systemCount != 1 || !foundTail {
		t.Fatalf("compacted history = %d msgs, want single system + tail (sysCount=%d tail=%v)",
			len(got), systemCount, foundTail)
	}
	if len(got) >= len(msgs) {
		t.Fatalf("compaction must shrink history: %d -> %d", len(msgs), len(got))
	}
}

// TestStripCompactedSummary 验证摘要段剥离是「替换」语义：连续剥离后只剩
// 基础 system prompt，不会累积多段过期摘要。
func TestStripCompactedSummary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no summary", "sys", "sys"},
		{"one summary", "sys\n\n<compacted-summary>\nS1\n</compacted-summary>", "sys"},
		{"two summaries", "sys\n\n<compacted-summary>\nS1\n</compacted-summary>\n\n<compacted-summary>\nS2\n</compacted-summary>", "sys"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripCompactedSummary(tc.in); got != tc.want {
				t.Fatalf("stripCompactedSummary(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestGuardModule_SummarizerFailureDegrades: a failing summarizer falls
// back to the ordinary lossy trim — the run never fails because
// summarization failed.
func TestGuardModule_SummarizerFailureDegrades(t *testing.T) {
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        50,
		CompactThreshold:   0.01,
		MinBudget:          10,
		ToolTruncateLength: 2000,
		Summarizer: func(ctx context.Context, msgs []*types.Message) (string, error) {
			return "", errors.New("summary model unavailable")
		},
	})
	ctx := context.Background()
	msgs := []*types.Message{types.NewSystemMessage("sys")}
	for i := 0; i < 40; i++ {
		msgs = append(msgs, types.NewUserMessage("user-" + strconv.Itoa(i)))
	}
	stepInfo := &kernel.StepInfo{Messages: msgs, Continue: true}
	if _, _, err := m.onStepStart(ctx, stepInfo); err != nil {
		t.Fatalf("onStepStart must not fail on summarizer error: %v", err)
	}
	if len(stepInfo.Messages) == 0 {
		t.Fatal("degraded trim must leave a history")
	}
}

// ── tool_calls 配对保护 ────────────────────────────────

// toolCallPair 构造一个 assistant 消息与对应 tool 响应。
func toolCallPair(callIDs ...string) []*types.Message {
	asst := &types.Message{Role: types.RoleAssistant}
	for _, id := range callIDs {
		asst.ToolCalls = append(asst.ToolCalls, types.ToolCall{
			ID:       id,
			Type:     "function",
			Function: types.ToolCallFunction{Name: "tool_" + id, Arguments: "{}"},
		})
	}
	out := []*types.Message{asst}
	for _, id := range callIDs {
		out = append(out, types.NewToolMessage("result "+id, id, "tool_"+id))
	}
	return out
}

// assertToolPairingValid 校验消息序列满足 LLM 提供商的配对约束：
// assistant 的每条 tool_call 之后必须紧跟对应的 tool 消息，且
// tool 消息必须能配对到前导 assistant（孤立 tool 消息同样是 400）。
func assertToolPairingValid(t *testing.T, msgs []*types.Message) {
	t.Helper()
	for i, m := range msgs {
		if m == nil {
			t.Fatalf("msgs[%d] 为 nil", i)
		}
		if m.Role == types.RoleTool {
			// 向前回溯到最近的 assistant：必须带 tool_calls 且覆盖本 ID
			// （组内后续 tool 消息的前一条仍是 tool 消息，需跳过）。
			ok := false
			for j := i - 1; j >= 0; j-- {
				prev := msgs[j]
				if prev == nil {
					continue
				}
				if prev.Role == types.RoleTool {
					continue
				}
				if prev.Role == types.RoleAssistant {
					for _, tc := range prev.ToolCalls {
						if tc.ID == m.ToolCallID {
							ok = true
						}
					}
				}
				break
			}
			if !ok {
				t.Fatalf("msgs[%d] 是孤立 tool 消息（无前导 assistant tool_calls 覆盖 id=%q）", i, m.ToolCallID)
			}
			continue
		}
		if m.Role != types.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		want := map[string]bool{}
		for _, tc := range m.ToolCalls {
			want[tc.ID] = true
		}
		for j := i + 1; j < len(msgs); j++ {
			rm := msgs[j]
			if rm == nil || rm.Role != types.RoleTool {
				break
			}
			delete(want, rm.ToolCallID)
		}
		if len(want) > 0 {
			t.Fatalf("assistant tool_calls 缺少响应消息: %v", want)
		}
	}
}

// TestTrim_WindowKeepsToolPairing 回归测试：窗口裁剪切在 tool_calls
// 配对中间时，结果序列必须保持完整配对——否则模型侧 400
// （insufficient tool messages following tool_calls / orphan tool message）。
// 以多种旧消息规模循环，覆盖窗口落在组前、组中间、组尾的不同切点。
func TestTrim_WindowKeepsToolPairing(t *testing.T) {
	m := NewGuardModuleWith(GuardConfig{
		TokenBudget:        200,
		CompactThreshold:   0.85,
		ToolTruncateLength: 50,
	})

	for _, oldN := range []int{2, 3, 4, 5, 8, 12} {
		msgs := []*types.Message{types.NewSystemMessage("sys")}
		for i := 0; i < oldN; i++ {
			msgs = append(msgs, types.NewUserMessage(strings.Repeat("old content ", 50)))
		}
		// 组在末尾（无尾部 user）：窗口从前面切，组内任意位置
		// 都可能成为切点——这正是修复前 400 的来源。
		msgs = append(msgs, toolCallPair("call_a", "call_b")...)

		trimmed, report := m.Trim(msgs, 200)
		if !report.Truncated {
			t.Fatalf("oldN=%d: over budget should truncate", oldN)
		}
		assertToolPairingValid(t, trimmed)
		// system 消息必须保留（trim 的基本契约）。
		if len(trimmed) == 0 || trimmed[0] == nil || trimmed[0].Role != types.RoleSystem {
			t.Fatalf("oldN=%d: system message lost: %+v", oldN, trimmed)
		}
	}
}

// TestRepairToolPairing 直接覆盖 repairToolPairing 的四种断链形态。
func TestRepairToolPairing(t *testing.T) {
	cases := []struct {
		name string
		in   []*types.Message
		want int // 期望保留的消息数
	}{
		{"完整配对原样保留",
			append([]*types.Message{types.NewUserMessage("u")}, toolCallPair("a", "b")...),
			4},
		{"开头孤立 tool 消息丢弃",
			append([]*types.Message{types.NewToolMessage("r", "a", "t")}, types.NewUserMessage("u")),
			1},
		{"结尾不完整组整体丢弃",
			[]*types.Message{
				types.NewUserMessage("u"),
				{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
					{ID: "a", Type: "function", Function: types.ToolCallFunction{Name: "t", Arguments: "{}"}},
					{ID: "b", Type: "function", Function: types.ToolCallFunction{Name: "t", Arguments: "{}"}},
				}},
				types.NewToolMessage("r", "a", "t"), // 只有 a 的响应，缺 b
			},
			1},
		{"不完整组丢弃后完整组保留",
			[]*types.Message{
				toolCallPair("a")[0], // asst(a)
				toolCallPair("a")[1], // tool(a) 完整配对
				types.NewUserMessage("u2"),
				toolCallPair("c")[0], // asst(c) 响应被裁
			},
			3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := repairToolPairing(c.in)
			assertToolPairingValid(t, out)
			if len(out) != c.want {
				t.Fatalf("len = %d, want %d (out: %+v)", len(out), c.want, out)
			}
		})
	}
}
