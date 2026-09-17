package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// plainAgent is NOT resumable: it only implements kernel.Agent.
type plainAgent struct{}

func (a *plainAgent) Name() string        { return "plain" }
func (a *plainAgent) Description() string { return "" }
func (a *plainAgent) Run(context.Context, *types.AgentInput, kernel.Runtime) *kernel.Result {
	return &kernel.Result{Content: "ran"}
}

// TestResume_MissingCheckpoint (D3): resuming an unknown id surfaces the
// store's not-found error instead of pretending success.
func TestResume_MissingCheckpoint(t *testing.T) {
	r := NewRunner(&plainAgent{}, nil)
	store := newMemCheckpointStore()
	r = NewRunner(&plainAgent{}, nil, WithRunnerCheckpointStore(store))

	_, err := r.Resume(context.Background(), "nope")
	if !errors.Is(err, kernel.ErrCheckpointNotFound) {
		t.Fatalf("Resume(unknown) = %v, want ErrCheckpointNotFound", err)
	}
}

// TestResume_NonResumableAgentWithStateFails (D3): a checkpoint carrying
// policy state cannot resume on an agent that does not implement
// kernel.Resumable — the state would be silently discarded. The resume must
// fail loudly instead.
func TestResume_NonResumableAgentWithStateFails(t *testing.T) {
	store := newMemCheckpointStore()
	cp := resumeCP("cp-1")
	cp.State = []byte(`{"v":1}`)
	if err := store.Save(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(&plainAgent{}, nil, WithRunnerCheckpointStore(store))

	_, err := r.Resume(context.Background(), "cp-1")
	if err == nil {
		t.Fatal("resuming a stateful checkpoint on a non-resumable agent must fail")
	}
	if !store.has("cp-1") {
		t.Fatal("the checkpoint must survive the failed resume")
	}
}

// TestResume_StatefulCheckpointOnNonResumableAgentWithoutStateRuns (D3):
// a stateless checkpoint is still resumable by any agent (only messages
// are carried).
func TestResume_StatefulCheckpointOnNonResumableAgentWithoutStateRuns(t *testing.T) {
	store := newMemCheckpointStore()
	cp := resumeCP("cp-2")
	cp.State = nil
	if err := store.Save(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(&plainAgent{}, nil, WithRunnerCheckpointStore(store))

	info, err := r.Resume(context.Background(), "cp-2")
	if err != nil {
		t.Fatalf("stateless resume must succeed: %v", err)
	}
	if info == nil || info.Result == nil || info.Result.Content != "ran" {
		t.Fatalf("info = %+v", info)
	}
}

// TestStream_ContextCancelEndsWithErrorEvent (D3): canceling the stream's
// context interrupts the run and the handle drains with an error event.
func TestStream_ContextCancelEndsWithErrorEvent(t *testing.T) {
	r := NewRunner(&blockingAgent{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	handle, err := r.Stream(ctx, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	cancel()
	var sawError bool
	for {
		ev, ok := handle.Next()
		if !ok {
			break
		}
		if ev.Type == types.EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("an interrupted stream must deliver an error event")
	}
}
