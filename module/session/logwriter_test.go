package session

import (
	"context"
	"testing"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/module/projection"
	"github.com/go-gocel/gocel/module/sessionstats"
)

// registrar collects the hooks LogWriter registers.
type writerRegistrar struct {
	agentStart  kernel.AgentStartHook
	stepStart   kernel.StepHook
	stepEnd     kernel.StepHook
	modelResult kernel.ModelResultHookFunc
	toolCall    kernel.ToolCallHook
	toolResult  kernel.ToolResultHookFunc
}

func (r *writerRegistrar) OnAgentStart(fn kernel.AgentStartHook) func()       { r.agentStart = fn; return nil }
func (r *writerRegistrar) OnAgentEnd(fn kernel.AgentEndHook) func()           { return nil }
func (r *writerRegistrar) OnMessagesBuilt(fn kernel.MessagesHook) func()      { return nil }
func (r *writerRegistrar) OnStepStart(fn kernel.StepHook) func()              { r.stepStart = fn; return nil }
func (r *writerRegistrar) OnStepEnd(fn kernel.StepHook) func()                { r.stepEnd = fn; return nil }
func (r *writerRegistrar) OnModelCall(fn kernel.ModelCallHook) func()         { return nil }
func (r *writerRegistrar) OnModelResult(fn kernel.ModelResultHookFunc) func() { r.modelResult = fn; return nil }
func (r *writerRegistrar) OnToolCall(fn kernel.ToolCallHook) func()           { r.toolCall = fn; return nil }
func (r *writerRegistrar) OnToolResult(fn kernel.ToolResultHookFunc) func()   { r.toolResult = fn; return nil }
func (r *writerRegistrar) OnDecision(fn kernel.DecisionHook) func()           { return nil }

// TestLogWriter_ProducesStatsEvents: the full integration — LogWriter
// translates lifecycle hooks into log events, and the sessionstats Unit
// folds them into real statistics. This is the producer→log→projection
// chain that makes sessionstats work in production.
func TestLogWriter_ProducesStatsEvents(t *testing.T) {
	log := coresession.NewLog("s1")
	w := NewLogWriter(log)
	r := &writerRegistrar{}
	w.Register(r)
	ctx := context.Background()

	// Attach the projection BEFORE driving the lifecycle: the registry
	// folds events from the moment of attachment (G8 semantics).
	reg := projection.New()
	if err := reg.Register(sessionstats.NewUnit()); err != nil {
		t.Fatal(err)
	}
	detach, err := reg.Attach(log)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()

	// Drive the lifecycle exactly as the engine would: model call with
	// usage, then the step's tool call, then the step end.
	_, _, _ = r.agentStart(ctx, &kernel.AgentRunInfo{AgentName: "agent-a"})
	_, _, _ = r.stepStart(ctx, &kernel.StepInfo{AgentName: "agent-a", StepIndex: 1, MaxSteps: 10})
	_, _, _ = r.modelResult(ctx, &kernel.ModelCallInfo{
		Response: types.NewAssistantMessage("ok"),
		Usage:    &types.TokenUsage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150},
	})
	_, _, _ = r.toolCall(ctx, &kernel.ToolCallInfo{Name: "grep", StepIndex: 1})
	_, _, _ = r.toolResult(ctx, &kernel.ToolCallInfo{Name: "grep", StepIndex: 1, Result: "match"})
	_, _, _ = r.stepEnd(ctx, &kernel.StepInfo{AgentName: "agent-a", StepIndex: 1, HasToolCalls: true})

	// The log carries the events.
	if log.Len() != 6 {
		t.Fatalf("log len = %d, want 6 lifecycle events", log.Len())
	}

	// The projection unit folds them into statistics — including the real
	// token numbers from the model-call usage.
	snap := reg.Snapshot("s1")
	st, ok := snap["session_stats"].(*sessionstats.Stats)
	if !ok {
		t.Fatalf("snapshot = %+v, want session_stats", snap)
	}
	if st.Steps != 1 || st.Turns != 1 || st.ToolCalls != 1 {
		t.Fatalf("stats = %+v, want 1 step / 1 turn / 1 tool call", st)
	}
	if st.PromptTokens != 120 || st.OutputTokens != 30 || st.TotalTokens != 150 {
		t.Fatalf("stats tokens = %d/%d/%d, want 120/30/150", st.PromptTokens, st.OutputTokens, st.TotalTokens)
	}
}

// TestLogWriter_ModelUsageAccumulates: every model call's usage rides into
// the log as a session/model_usage event; the stats unit accumulates the
// deltas across calls.
func TestLogWriter_ModelUsageAccumulates(t *testing.T) {
	log := coresession.NewLog("s1")
	w := NewLogWriter(log)
	r := &writerRegistrar{}
	w.Register(r)
	ctx := context.Background()

	reg := projection.New()
	if err := reg.Register(sessionstats.NewUnit()); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Attach(log); err != nil {
		t.Fatal(err)
	}

	_, _, _ = r.modelResult(ctx, &kernel.ModelCallInfo{
		Usage: &types.TokenUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
	})
	_, _, _ = r.modelResult(ctx, &kernel.ModelCallInfo{
		Usage: &types.TokenUsage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70},
	})
	_, _, _ = r.modelResult(ctx, &kernel.ModelCallInfo{Response: types.NewAssistantMessage("no usage")})

	snap := reg.Snapshot("s1")
	st := snap["session_stats"].(*sessionstats.Stats)
	if st.PromptTokens != 150 || st.OutputTokens != 30 || st.TotalTokens != 180 {
		t.Fatalf("accumulated tokens = %d/%d/%d, want 150/30/180", st.PromptTokens, st.OutputTokens, st.TotalTokens)
	}
}

// TestLogWriter_ProducesStatsEvents via the hook chain: verify the
// registrar really received the hooks (the writer registers all six).
func TestLogWriter_RegistersAllHooks(t *testing.T) {
	log := coresession.NewLog("s1")
	w := NewLogWriter(log)
	r := &writerRegistrar{}
	w.Register(r)
	if r.agentStart == nil || r.stepStart == nil || r.stepEnd == nil || r.modelResult == nil || r.toolCall == nil || r.toolResult == nil {
		t.Fatal("LogWriter must register all six lifecycle hooks")
	}
}
