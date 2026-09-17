package repeattool

import (
	"context"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// registrar collects the ToolResult and MessagesBuilt hooks.
type registrar struct {
	toolResult kernel.ToolResultHookFunc
	messages   kernel.MessagesHook
}

func (r *registrar) OnAgentStart(kernel.AgentStartHook) func()       { return nil }
func (r *registrar) OnAgentEnd(kernel.AgentEndHook) func()           { return nil }
func (r *registrar) OnMessagesBuilt(fn kernel.MessagesHook) func()   { r.messages = fn; return nil }
func (r *registrar) OnStepStart(kernel.StepHook) func()              { return nil }
func (r *registrar) OnStepEnd(kernel.StepHook) func()                { return nil }
func (r *registrar) OnModelCall(kernel.ModelCallHook) func()         { return nil }
func (r *registrar) OnModelResult(kernel.ModelResultHookFunc) func() { return nil }
func (r *registrar) OnToolCall(kernel.ToolCallHook) func()           { return nil }
func (r *registrar) OnToolResult(fn kernel.ToolResultHookFunc) func() {
	r.toolResult = fn
	return nil
}
func (r *registrar) OnDecision(kernel.DecisionHook) func() { return nil }

func agentCtx() context.Context {
	// AgentName is read from the AgentContext; use a stub via a small
	// wrapper is overkill — the module tolerates a nil context (agent "").
	return context.Background()
}

func resultCall(r *registrar, name, args string) {
	_, _, _ = r.toolResult(context.Background(), &kernel.ToolCallInfo{Name: name, Args: args})
}

func builtMsgs(r *registrar) []*types.Message {
	_, msgs, _ := r.messages(context.Background(), []*types.Message{types.NewUserMessage("go")})
	return msgs
}

// TestRepeat_RemindsAtThresholds: the reminder appears at the first
// threshold, escalates later, and the tool result is never rewritten.
func TestRepeat_RemindsAtThresholds(t *testing.T) {
	m := New(WithThresholds([]int{3, 6}))
	r := &registrar{}
	m.Register(r)

	// Calls 1-2: no reminder yet.
	resultCall(r, "grep", `{"pattern":"x"}`)
	resultCall(r, "grep", `{"pattern":"x"}`)
	if msgs := builtMsgs(r); len(msgs) != 1 {
		t.Fatalf("before threshold msgs = %d, want 1 (no injection)", len(msgs))
	}
	// Call 3: first reminder.
	resultCall(r, "grep", `{"pattern":"x"}`)
	msgs := builtMsgs(r)
	if len(msgs) != 2 || msgs[1].Role != types.RoleSystem || !strings.Contains(msgs[1].Content, "grep") {
		t.Fatalf("threshold msgs = %+v, want [user, system reminder]", msgs)
	}
	if !strings.Contains(msgs[1].Content, "3 times") {
		t.Fatalf("reminder = %q, want the 3-times escalation", msgs[1].Content)
	}
	// Calls 4-5: no new reminder (only at thresholds).
	resultCall(r, "grep", `{"pattern":"x"}`)
	resultCall(r, "grep", `{"pattern":"x"}`)
	if msgs := builtMsgs(r); len(msgs) != 1 {
		t.Fatalf("between thresholds msgs = %d, want 1", len(msgs))
	}
	// Call 6: second reminder.
	resultCall(r, "grep", `{"pattern":"x"}`)
	msgs = builtMsgs(r)
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content, "6 times") {
		t.Fatalf("second escalation = %+v", msgs)
	}
}

// TestRepeat_ArgOrderInsensitive: identical logical arguments with
// different key order still count as repeats (normalized comparison).
func TestRepeat_ArgOrderInsensitive(t *testing.T) {
	m := New(WithThresholds([]int{3}))
	r := &registrar{}
	m.Register(r)
	resultCall(r, "read", `{"limit":10,"path":"/a"}`)
	resultCall(r, "read", `{"path":"/a","limit":10}`)
	resultCall(r, "read", `{"path":"/a","limit":10}`)
	msgs := builtMsgs(r)
	if len(msgs) != 2 {
		t.Fatalf("normalized repeat not detected: %d msgs, want 2", len(msgs))
	}
}

// TestRepeat_ChainResetsOnDifferentCall: a different tool or arguments
// resets the chain.
func TestRepeat_ChainResetsOnDifferentCall(t *testing.T) {
	m := New(WithThresholds([]int{3}))
	r := &registrar{}
	m.Register(r)
	resultCall(r, "grep", `{"pattern":"x"}`)
	resultCall(r, "grep", `{"pattern":"x"}`)
	resultCall(r, "grep", `{"pattern":"y"}`) // different args: reset
	resultCall(r, "grep", `{"pattern":"y"}`)
	resultCall(r, "grep", `{"pattern":"y"}`)
	msgs := builtMsgs(r)
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content, "grep") {
		t.Fatalf("chain reset: msgs = %+v, want one reminder for the new chain", msgs)
	}
}

// TestRepeat_ToolResultUntouched: the tool result passes through unchanged.
func TestRepeat_ToolResultUntouched(t *testing.T) {
	m := New(WithThresholds([]int{3}))
	r := &registrar{}
	m.Register(r)
	info := &kernel.ToolCallInfo{Name: "grep", Args: `{"pattern":"x"}`, Result: "match"}
	for i := 0; i < 3; i++ {
		_, out, err := r.toolResult(context.Background(), info)
		if err != nil || out == nil || out.Result != "match" {
			t.Fatalf("tool result must pass through untouched, got %+v, %v", out, err)
		}
	}
}
