package msgcheck

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

type registrar struct {
	modelCall kernel.ModelCallHook
}

func (r *registrar) OnAgentStart(kernel.AgentStartHook) func()       { return nil }
func (r *registrar) OnAgentEnd(kernel.AgentEndHook) func()           { return nil }
func (r *registrar) OnMessagesBuilt(kernel.MessagesHook) func()      { return nil }
func (r *registrar) OnStepStart(kernel.StepHook) func()              { return nil }
func (r *registrar) OnStepEnd(kernel.StepHook) func()                { return nil }
func (r *registrar) OnModelCall(fn kernel.ModelCallHook) func()      { r.modelCall = fn; return nil }
func (r *registrar) OnModelResult(kernel.ModelResultHookFunc) func() { return nil }
func (r *registrar) OnToolCall(kernel.ToolCallHook) func()           { return nil }
func (r *registrar) OnToolResult(kernel.ToolResultHookFunc) func()   { return nil }
func (r *registrar) OnDecision(kernel.DecisionHook) func()           { return nil }

func TestSingleSystemValid(t *testing.T) {
	m := New()
	r := &registrar{}
	m.Register(r)
	_, _, err := r.modelCall(context.Background(), &kernel.ModelCallInfo{
		Messages: []*types.Message{types.NewSystemMessage("s"), types.NewUserMessage("u")},
	})
	if err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
}

func TestNoSystemValid(t *testing.T) {
	m := New()
	r := &registrar{}
	m.Register(r)
	_, _, err := r.modelCall(context.Background(), &kernel.ModelCallInfo{
		Messages: []*types.Message{types.NewUserMessage("u")},
	})
	if err != nil {
		t.Fatalf("systemless list rejected: %v", err)
	}
}

func TestSecondSystemRejected(t *testing.T) {
	m := New()
	r := &registrar{}
	m.Register(r)
	_, _, err := r.modelCall(context.Background(), &kernel.ModelCallInfo{
		Messages: []*types.Message{types.NewSystemMessage("s1"), types.NewSystemMessage("s2")},
	})
	if err == nil {
		t.Fatal("second system not rejected")
	}
}

func TestSystemNotAtIndexZeroRejected(t *testing.T) {
	m := New()
	r := &registrar{}
	m.Register(r)
	_, _, err := r.modelCall(context.Background(), &kernel.ModelCallInfo{
		Messages: []*types.Message{types.NewUserMessage("u"), types.NewSystemMessage("s")},
	})
	if err == nil {
		t.Fatal("system at index 1 not rejected")
	}
}
