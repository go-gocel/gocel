package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

// fakeAgent records the input/context it received so tests can observe
// runner wiring.
type fakeAgent struct {
	name      string
	lastInput *types.AgentInput
	lastState kernel.StateManager
}

func (a *fakeAgent) Name() string        { return a.name }
func (a *fakeAgent) Description() string { return "fake" }
func (a *fakeAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	a.lastInput = input
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		a.lastState = ac.State()
	}
	return &kernel.Result{Content: "done", Messages: input.Messages}
}

// TestStream_DoesNotMutateCallerInput: Stream wires the sender on a copy —
// the caller-owned input must stay untouched (C2), so reuse and concurrent
// use of the original stay race-free.
func TestStream_DoesNotMutateCallerInput(t *testing.T) {
	fa := &fakeAgent{name: "a"}
	r := NewRunner(fa, nil)
	input := &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}}

	h, err := r.Stream(context.Background(), input)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if input.EnableStreaming {
		t.Fatalf("Stream mutated caller input: EnableStreaming = true")
	}
	if input.StreamSender != nil {
		t.Fatalf("Stream mutated caller input: StreamSender set")
	}
	if got := h.Result(); got == nil || got.Err != nil {
		t.Fatalf("stream result: %+v", got)
	}
	// The agent must have received the wired copy.
	if fa.lastInput == input {
		t.Fatalf("agent received the caller's input pointer, want a copy")
	}
	if fa.lastInput == nil || !fa.lastInput.EnableStreaming || fa.lastInput.StreamSender == nil {
		t.Fatalf("agent input not wired: %+v", fa.lastInput)
	}
}

// TestRunner_AgentContextStateIsRuntimeState: the AgentContext must expose
// the Runtime's shared state — the contract's single source of truth (C2).
func TestRunner_AgentContextStateIsRuntimeState(t *testing.T) {
	sm := runtime.NewInMemoryState()
	fa := &fakeAgent{name: "a"}
	r := NewRunner(fa, nil, WithStateManager(sm))

	info := r.Run(context.Background(), &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}})
	if info.Err != nil {
		t.Fatalf("Run: %v", info.Err)
	}
	if fa.lastState == nil {
		t.Fatalf("agent saw no AgentContext state")
	}
	if fa.lastState != sm {
		t.Fatalf("AgentContext.State() is not the Runtime's state manager")
	}
}

// panicAgent panics inside Run.
type panicAgent struct{ name string }

func (a *panicAgent) Name() string        { return a.name }
func (a *panicAgent) Description() string { return "panics" }
func (a *panicAgent) Run(context.Context, *types.AgentInput, kernel.Runtime) *kernel.Result {
	panic("agent bug")
}

// TestRunner_AgentPanicBecomesResult: a panicking agent must surface as an
// error result (AgentEnd hooks still fire), never crash the process (C3).
func TestRunner_AgentPanicBecomesResult(t *testing.T) {
	r := NewRunner(&panicAgent{name: "p"}, nil)
	info := r.Run(context.Background(), &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}})
	if info.Err == nil {
		t.Fatal("panicking agent = nil error, want a panic error result")
	}
	if !strings.Contains(info.Err.Error(), "panicked") {
		t.Fatalf("error = %v, want panic marker", info.Err)
	}
	if info.Result == nil || info.Result.Reason != kernel.TerminateError {
		t.Fatalf("result = %+v, want TerminateError", info.Result)
	}
}
