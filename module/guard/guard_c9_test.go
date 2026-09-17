package guard

import (
	"context"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

type stepRegistrar struct {
	step kernel.StepHook
}

func (r *stepRegistrar) OnAgentStart(fn kernel.AgentStartHook) func()       { return nil }
func (r *stepRegistrar) OnAgentEnd(fn kernel.AgentEndHook) func()           { return nil }
func (r *stepRegistrar) OnMessagesBuilt(fn kernel.MessagesHook) func()      { return nil }
func (r *stepRegistrar) OnStepStart(fn kernel.StepHook) func()              { r.step = fn; return func() {} }
func (r *stepRegistrar) OnStepEnd(fn kernel.StepHook) func()                { return nil }
func (r *stepRegistrar) OnModelCall(fn kernel.ModelCallHook) func()         { return nil }
func (r *stepRegistrar) OnModelResult(fn kernel.ModelResultHookFunc) func() { return nil }
func (r *stepRegistrar) OnToolCall(fn kernel.ToolCallHook) func()           { return nil }
func (r *stepRegistrar) OnToolResult(fn kernel.ToolResultHookFunc) func()   { return nil }
func (r *stepRegistrar) OnDecision(fn kernel.DecisionHook) func()           { return nil }

// TestOnTrim_ReportsOnlyActuallyDropped is the C9 regression: compaction
// clones messages, so pointer comparison used to report EVERY message as
// dropped. The callback must receive only the messages truly absent from
// the trimmed list.
func TestOnTrim_ReportsOnlyActuallyDropped(t *testing.T) {
	var dropped []*types.Message
	mod := NewGuardModuleWith(GuardConfig{
		TokenBudget:        400,
		CompactThreshold:   0.9,
		MinBudget:          50,
		ToolTruncateLength: 20,
		OnTrim: func(d []*types.Message, _ *types.TrimReport) {
			dropped = append([]*types.Message(nil), d...)
		},
	})
	reg := &stepRegistrar{}
	mod.Register(reg)

	// A history with a system message, one long tool message (compactable),
	// and many user messages — clearly over budget.
	msgs := []*types.Message{types.NewSystemMessage("sys")}
	msgs = append(msgs, types.NewToolMessage(strings.Repeat("x", 500), "call-1", "t"))
	for i := 0; i < 30; i++ {
		msgs = append(msgs, types.NewUserMessage(strings.Repeat("y", 100)))
	}

	_, _, err := reg.step(context.Background(), &kernel.StepInfo{Messages: msgs})
	if err != nil {
		t.Fatalf("step hook = %v", err)
	}
	if len(dropped) == 0 {
		t.Fatal("no drop reported despite over-budget history")
	}
	if len(dropped) >= len(msgs) {
		t.Fatalf("dropped %d of %d messages — every message misreported as dropped (C9)", len(dropped), len(msgs))
	}
	for _, d := range dropped {
		if d.Role == types.RoleSystem {
			t.Fatal("system message must never be reported as dropped")
		}
	}
}
