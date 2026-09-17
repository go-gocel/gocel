package checkpoint

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// countingStore wraps an in-memory store to count Save calls.
type countingStore struct {
	*InMemoryCheckpointStore
	saves int
}

func (s *countingStore) Save(ctx context.Context, cp *types.Checkpoint) error {
	s.saves++
	return s.InMemoryCheckpointStore.Save(ctx, cp)
}

// TestAutoSaveModule_SavesPeriodically proves the periodic save semantics:
// a checkpoint is saved every interval-th step (step > 0), reusing one ID so
// the store always holds the most recent recovery point.
func TestAutoSaveModule_SavesPeriodically(t *testing.T) {
	store := &countingStore{InMemoryCheckpointStore: NewInMemoryCheckpointStore()}
	m := NewAutoSaveModule(store) // default interval 5
	ctx := context.Background()

	for i := 1; i <= 12; i++ {
		info := &kernel.StepInfo{
			AgentName: "agent-x",
			Messages:  []*types.Message{types.NewUserMessage(fmt.Sprintf("msg %d", i))},
			StepIndex: i,
		}
		if _, _, err := m.onStepEnd(ctx, info); err != nil {
			t.Fatalf("onStepEnd(%d): %v", i, err)
		}
	}

	if store.saves != 2 {
		t.Fatalf("saved %d times, want 2 (steps 5 and 10)", store.saves)
	}
	ids, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("store holds %d checkpoints, want 1 (ID reused for the latest point)", len(ids))
	}
	cp, err := store.Load(ctx, ids[0])
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp.StepIndex != 10 {
		t.Fatalf("checkpoint StepIndex = %d, want 10 (latest saved step)", cp.StepIndex)
	}
	if cp.AgentName != "agent-x" {
		t.Fatalf("AgentName = %q, want agent-x", cp.AgentName)
	}
	if len(cp.Messages) != 1 || cp.Messages[0].Content != "msg 10" {
		t.Fatalf("checkpoint messages = %v, want the messages of step 10", cp.Messages)
	}
}

// TestAutoSaveModule_SkipsEarlyAndNonIntervalSteps proves step 0 and
// non-interval steps are not saved, and a custom interval is honored.
func TestAutoSaveModule_SkipsEarlyAndNonIntervalSteps(t *testing.T) {
	store := &countingStore{InMemoryCheckpointStore: NewInMemoryCheckpointStore()}
	m := NewAutoSaveModule(store, WithInterval(2))
	ctx := context.Background()

	for i := 0; i <= 6; i++ {
		info := &kernel.StepInfo{
			AgentName: "agent-y",
			Messages:  []*types.Message{types.NewUserMessage("m")},
			StepIndex: i,
		}
		if _, _, err := m.onStepEnd(ctx, info); err != nil {
			t.Fatalf("onStepEnd(%d): %v", i, err)
		}
	}

	if store.saves != 3 {
		t.Fatalf("saved %d times, want 3 (steps 2, 4, 6)", store.saves)
	}
	cp, err := store.Load(ctx, mustSingleID(t, store))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp.StepIndex != 6 {
		t.Fatalf("checkpoint StepIndex = %d, want 6", cp.StepIndex)
	}
}

// TestAutoSaveModule_NilStoreIsNoop proves a nil store degrades to no-op.
func TestAutoSaveModule_NilStoreIsNoop(t *testing.T) {
	m := NewAutoSaveModule(nil)
	info := &kernel.StepInfo{
		AgentName: "agent-z",
		Messages:  []*types.Message{types.NewUserMessage("m")},
		StepIndex: 5,
	}
	if _, _, err := m.onStepEnd(context.Background(), info); err != nil {
		t.Fatalf("onStepEnd: %v", err)
	}
}

// TestAutoSaveModule_WithSessionID proves the session tag lands in the
// persisted checkpoint, so consumers sharing one store across sessions can
// locate each session's latest recovery point.
func TestAutoSaveModule_WithSessionID(t *testing.T) {
	store := &countingStore{InMemoryCheckpointStore: NewInMemoryCheckpointStore()}
	m := NewAutoSaveModule(store, WithSessionID("sess-web-42"))
	info := &kernel.StepInfo{
		AgentName: "agent-x",
		Messages:  []*types.Message{types.NewUserMessage("m")},
		StepIndex: 5,
	}
	if _, _, err := m.onStepEnd(context.Background(), info); err != nil {
		t.Fatalf("onStepEnd: %v", err)
	}
	id := mustSingleID(t, store)
	cp, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp.SessionID != "sess-web-42" {
		t.Fatalf("checkpoint SessionID = %q, want sess-web-42", cp.SessionID)
	}
}

func mustSingleID(t *testing.T, store *countingStore) string {
	t.Helper()
	ids, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("store holds %d checkpoints, want 1", len(ids))
	}
	return ids[0]
}

// TestBarrier_ModelCallSavesBeforeRequest: with BarrierModelCall, the
// model-request boundary saves a checkpoint BEFORE the call — the intent is
// durable before the request is sent.
func TestBarrier_ModelCallSavesBeforeRequest(t *testing.T) {
	store := &countingStore{InMemoryCheckpointStore: NewInMemoryCheckpointStore()}
	m := NewAutoSaveModule(store, WithBarriers(BarrierModelCall))
	info := &kernel.ModelCallInfo{
		AgentName: "agent-x",
		Messages:  []*types.Message{types.NewUserMessage("pending request")},
	}
	if _, _, err := m.onModelCall(context.Background(), info); err != nil {
		t.Fatalf("onModelCall: %v", err)
	}
	cp, err := store.Load(context.Background(), mustSingleID(t, store))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cp.Messages) != 1 || cp.Messages[0].Content != "pending request" {
		t.Fatalf("barrier checkpoint must carry the request-producing messages, got %+v", cp.Messages)
	}
}

// TestBarrier_ToolCallSavesBeforeSideEffect: with BarrierToolCall, the tool
// boundary saves before execution — the intent is durable before the side
// effect runs.
func TestBarrier_ToolCallSavesBeforeSideEffect(t *testing.T) {
	store := &countingStore{InMemoryCheckpointStore: NewInMemoryCheckpointStore()}
	m := NewAutoSaveModule(store, WithBarriers(BarrierToolCall))
	info := &kernel.ToolCallInfo{
		Name:      "write",
		AgentName: "agent-x",
		StepIndex: 3,
		Args:      `{"path":"/tmp/x","content":"y"}`,
	}
	if _, _, err := m.onToolCall(context.Background(), info); err != nil {
		t.Fatalf("onToolCall: %v", err)
	}
	cp, err := store.Load(context.Background(), mustSingleID(t, store))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp.StepIndex != 3 {
		t.Fatalf("barrier checkpoint StepIndex = %d, want 3", cp.StepIndex)
	}
}

// TestBarrier_FailsClosedOnSaveError: a barrier save failure BLOCKS the
// boundary — an undurable execution intent never proceeds (DSH
// session-checkpoint-policy fail-closed).
func TestBarrier_FailsClosedOnSaveError(t *testing.T) {
	failing := &failingStore{}
	m := NewAutoSaveModule(failing, WithBarriers(BarrierModelCall|BarrierToolCall))
	if _, _, err := m.onModelCall(context.Background(), &kernel.ModelCallInfo{AgentName: "a"}); err == nil {
		t.Fatal("model barrier with failing store must block the call")
	}
	if _, _, err := m.onToolCall(context.Background(), &kernel.ToolCallInfo{Name: "write"}); err == nil {
		t.Fatal("tool barrier with failing store must block the call")
	}
}

// failingStore always fails saves — the barrier fail-closed probe.
type failingStore struct{}

func (s *failingStore) Save(context.Context, *types.Checkpoint) error { return fmt.Errorf("disk full") }
func (s *failingStore) Load(context.Context, string) (*types.Checkpoint, error) {
	return nil, fmt.Errorf("not found")
}
func (s *failingStore) Delete(context.Context, string) error          { return nil }
func (s *failingStore) List(context.Context) ([]string, error)        { return nil, nil }

// TestBarrier_DisabledByDefault: without WithBarriers the boundaries do not
// save — the module keeps its legacy step-interval behavior only.
func TestBarrier_DisabledByDefault(t *testing.T) {
	store := &countingStore{InMemoryCheckpointStore: NewInMemoryCheckpointStore()}
	m := NewAutoSaveModule(store)
	if _, _, err := m.onModelCall(context.Background(), &kernel.ModelCallInfo{AgentName: "a"}); err != nil {
		t.Fatalf("onModelCall without barrier = %v, want pass-through", err)
	}
	if store.saves != 0 {
		t.Fatalf("saves = %d, want 0 (barriers off by default)", store.saves)
	}
}
