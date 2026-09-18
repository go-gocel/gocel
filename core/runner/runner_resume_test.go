package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// memCheckpointStore is a minimal in-memory CheckpointStore for resume tests.
type memCheckpointStore struct {
	mu    sync.Mutex
	items map[string]*types.Checkpoint
	// failDelete injects Delete failures (C8: delete errors must surface).
	failDelete bool
}

func newMemCheckpointStore() *memCheckpointStore {
	return &memCheckpointStore{items: make(map[string]*types.Checkpoint)}
}

func (s *memCheckpointStore) Save(_ context.Context, cp *types.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *cp
	s.items[cp.ID] = &c
	return nil
}

func (s *memCheckpointStore) Load(_ context.Context, id string) (*types.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.items[id]
	if !ok {
		return nil, kernel.ErrCheckpointNotFound
	}
	c := *cp
	return &c, nil
}

func (s *memCheckpointStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDelete {
		return errors.New("injected delete failure")
	}
	delete(s.items, id)
	return nil
}

func (s *memCheckpointStore) List(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *memCheckpointStore) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.items[id]
	return ok
}

// ── resume test agents ───────────────────────────────────────────────────
// Resume carries messages only — none of these agents implements a
// policy-session restore hook; they differ solely in run behavior.

type okRunAgent struct{}

func (a *okRunAgent) Name() string        { return "ok-run" }
func (a *okRunAgent) Description() string { return "run succeeds" }
func (a *okRunAgent) Run(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	return &kernel.Result{Content: "ran"}
}

type failRunAgent struct{}

func (a *failRunAgent) Name() string        { return "fail-run" }
func (a *failRunAgent) Description() string { return "run fails" }
func (a *failRunAgent) Run(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	return &kernel.Result{Err: errors.New("boom")}
}

// resumeCP builds a stateful checkpoint (MaxSteps 5 at step 1 → 4 steps
// remaining). State is deliberately set: the Runner carries it verbatim and
// never replays it, so its presence must not change resume behavior.
func resumeCP(id string) *types.Checkpoint {
	return &types.Checkpoint{
		ID: id, AgentName: "a", StepIndex: 1, MaxSteps: 5,
		Messages: []*types.Message{types.NewUserMessage("hi")},
		State:    []byte(`{"v":1}`),
	}
}

// TestResume_KeepsCheckpointOnRunFailure is the C8 regression: a resumed run
// that fails must keep the checkpoint so the user can retry — it is consumed
// only once the run actually succeeded.
func TestResume_KeepsCheckpointOnRunFailure(t *testing.T) {
	store := newMemCheckpointStore()
	_ = store.Save(context.Background(), resumeCP("cp-2"))
	r := NewRunner(&failRunAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

	if _, err := r.Resume(context.Background(), "cp-2"); err == nil {
		t.Fatal("Resume = nil, want run error")
	}
	if !store.has("cp-2") {
		t.Fatal("checkpoint consumed on failed run")
	}
}

// TestResume_DeletesCheckpointOnlyAfterSuccess: the checkpoint is consumed
// exactly when the resumed run succeeded.
func TestResume_DeletesCheckpointOnlyAfterSuccess(t *testing.T) {
	store := newMemCheckpointStore()
	_ = store.Save(context.Background(), resumeCP("cp-3"))
	r := NewRunner(&okRunAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

	if _, err := r.Resume(context.Background(), "cp-3"); err != nil {
		t.Fatalf("Resume = %v", err)
	}
	if store.has("cp-3") {
		t.Fatal("checkpoint kept after successful resume")
	}
}

// TestResume_SurfacesDeleteFailure: a cleanup failure after a successful run
// is reported, never swallowed.
func TestResume_SurfacesDeleteFailure(t *testing.T) {
	store := newMemCheckpointStore()
	store.failDelete = true
	_ = store.Save(context.Background(), resumeCP("cp-4"))
	r := NewRunner(&okRunAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

	_, err := r.Resume(context.Background(), "cp-4")
	if err == nil || !strings.Contains(err.Error(), "delete") {
		t.Fatalf("Resume = %v, want delete-failure surfaced", err)
	}
}

// TestResume_DeliversTerminalEvents pins the event contract for resume:
// StreamSender delivery is independent of generation mode, matching Run.
func TestResume_DeliversTerminalEvents(t *testing.T) {
	store := newMemCheckpointStore()
	_ = store.Save(context.Background(), resumeCP("cp-5"))
	r := NewRunner(&okRunAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

	var mu sync.Mutex
	var seen []types.EventType
	info, err := r.Resume(context.Background(), "cp-5", WithModifiedInput(&types.AgentInput{
		StreamSender: func(ev *types.Event) bool {
			mu.Lock()
			seen = append(seen, ev.Type)
			mu.Unlock()
			return true
		},
	}))
	if err != nil {
		t.Fatalf("Resume = %v", err)
	}
	_ = info
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, et := range seen {
		if et == types.EventFinish {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want EventFinish", seen)
	}
}

// ── resume gap cases ─────────────────────────────────────────────────────

// plainAgent is NOT resumable: it only implements kernel.Agent. It records
// the input it was handed so resume tests can observe what the Runner carried.
type plainAgent struct{ lastInput *types.AgentInput }

func (a *plainAgent) Name() string        { return "plain" }
func (a *plainAgent) Description() string { return "" }
func (a *plainAgent) Run(_ context.Context, input *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	a.lastInput = input
	return &kernel.Result{Content: "ran"}
}

// TestResume_MissingCheckpoint (D3): resuming an unknown id surfaces the
// store's not-found error instead of pretending success.
func TestResume_MissingCheckpoint(t *testing.T) {
	store := newMemCheckpointStore()
	r := NewRunner(&plainAgent{}, nil, WithRunnerCheckpointStore(store))

	_, err := r.Resume(context.Background(), "nope")
	if !errors.Is(err, kernel.ErrCheckpointNotFound) {
		t.Fatalf("Resume(unknown) = %v, want ErrCheckpointNotFound", err)
	}
}

// TestResume_CarriesMessagesNotState (D3): resume is message-carrying, not
// session-restoring — the checkpoint's State is never replayed, so a stateful
// checkpoint resumes exactly like a stateless one on a plain agent. What is
// carried: the saved messages, the system prompt and the remaining step budget.
func TestResume_CarriesMessagesNotState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state []byte
	}{
		{name: "stateful", state: []byte(`{"v":1}`)},
		{name: "stateless", state: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemCheckpointStore()
			cp := resumeCP("cp-" + tc.name)
			cp.State = tc.state
			if err := store.Save(context.Background(), cp); err != nil {
				t.Fatal(err)
			}
			agent := &plainAgent{}
			r := NewRunner(agent, nil, WithRunnerCheckpointStore(store))

			info, err := r.Resume(context.Background(), cp.ID)
			if err != nil {
				t.Fatalf("Resume = %v, want success", err)
			}
			if info == nil || info.Result == nil || info.Result.Content != "ran" {
				t.Fatalf("info = %+v", info)
			}
			if agent.lastInput == nil || len(agent.lastInput.Messages) != 1 ||
				agent.lastInput.Messages[0].Content != "hi" {
				t.Fatalf("resumed input = %+v, want the checkpoint's 1 message", agent.lastInput)
			}
			if agent.lastInput.MaxSteps != 4 {
				t.Fatalf("MaxSteps = %d, want 4 (5 - 1 saved step)", agent.lastInput.MaxSteps)
			}
		})
	}
}
