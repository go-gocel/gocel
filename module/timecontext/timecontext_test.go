package timecontext

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

type registrar struct {
	messages kernel.MessagesHook
}

func (r *registrar) OnAgentStart(kernel.AgentStartHook) func()             { return nil }
func (r *registrar) OnAgentEnd(kernel.AgentEndHook) func()                 { return nil }
func (r *registrar) OnMessagesBuilt(fn kernel.MessagesHook) func()         { r.messages = fn; return nil }
func (r *registrar) OnStepStart(kernel.StepHook) func()                    { return nil }
func (r *registrar) OnStepEnd(kernel.StepHook) func()                      { return nil }
func (r *registrar) OnModelCall(kernel.ModelCallHook) func()               { return nil }
func (r *registrar) OnModelResult(kernel.ModelResultHookFunc) func()       { return nil }
func (r *registrar) OnToolCall(kernel.ToolCallHook) func()                 { return nil }
func (r *registrar) OnToolResult(kernel.ToolResultHookFunc) func()         { return nil }
func (r *registrar) OnDecision(kernel.DecisionHook) func()                 { return nil }

// TestModule_InjectTimeHead: the current time is appended as a system
// message (merged into the single system by FireMessagesBuilt later).
func TestModule_InjectTimeHead(t *testing.T) {
	loc := time.FixedZone("T", 8*3600)
	m := New(WithLocation(loc), WithFormat("2006-01-02 15:04:05"))
	r := &registrar{}
	m.Register(r)

	before := time.Now().In(loc).Format("2006-01-02 15:04:05")
	ctx, msgs, err := r.messages(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx
	if len(msgs) != 2 || msgs[1].Role != types.RoleSystem {
		t.Fatalf("msgs = %+v, want [user, system]", msgs)
	}
	if !strings.Contains(msgs[1].Content, "Current time: ") {
		t.Fatalf("injected = %q, want Current time prefix", msgs[1].Content)
	}
	// The injected time must be within the run window (same formatted
	// second or the next — a slow machine may tick over).
	after := time.Now().In(loc).Format("2006-01-02 15:04:05")
	got := strings.TrimPrefix(msgs[1].Content, "Current time: ")
	if got != before && got != after {
		t.Fatalf("injected time = %q, outside [%q, %q]", got, before, after)
	}
	if msgs[0].Content != "hi" {
		t.Fatalf("user message must be preserved, got %q", msgs[0].Content)
	}
}

// TestModule_AppendsSecondSystem 验证模块把时间作为一条 system 消息追加到
// 末尾（不再前插/合并），合并交给 FireMessagesBuilt 的 collapseSystem。
func TestModule_AppendsSecondSystem(t *testing.T) {
	loc := time.FixedZone("T", 8*3600)
	m := New(WithLocation(loc), WithFormat("2006-01-02 15:04:05"))
	r := &registrar{}
	m.Register(r)

	msgs := []*types.Message{types.NewSystemMessage("base"), types.NewUserMessage("hi")}
	ctx, out, err := r.messages(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3 (system appended)", len(out))
	}
	if out[0].Role != types.RoleSystem || out[0].Content != "base" {
		t.Fatalf("out[0] = %+v, want original system", out[0])
	}
	if out[1].Role != types.RoleUser || out[1].Content != "hi" {
		t.Fatalf("out[1] = %+v, want user", out[1])
	}
	if out[2].Role != types.RoleSystem || !strings.HasPrefix(out[2].Content, "Current time: ") {
		t.Fatalf("out[2] = %+v, want appended system with Current time", out[2])
	}
}
