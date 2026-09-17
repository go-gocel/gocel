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

// ── resumable agents ─────────────────────────────────────────────────────

type restoreFailAgent struct{}

func (a *restoreFailAgent) Name() string        { return "restore-fail" }
func (a *restoreFailAgent) Description() string { return "restore fails" }
func (a *restoreFailAgent) Run(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	return &kernel.Result{Content: "ran"}
}

type restoreOKAgent struct{}

func (a *restoreOKAgent) Name() string        { return "restore-ok" }
func (a *restoreOKAgent) Description() string { return "restore works" }
func (a *restoreOKAgent) Run(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	return &kernel.Result{Content: "ran"}
}

type restoreOKRunFailAgent struct{}

func (a *restoreOKRunFailAgent) Name() string        { return "run-fail" }
func (a *restoreOKRunFailAgent) Description() string { return "run fails" }
func (a *restoreOKRunFailAgent) Run(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	return &kernel.Result{Err: errors.New("boom")}
}

func resumeCP(id string) *types.Checkpoint {
	return &types.Checkpoint{
		ID: id, AgentName: "a", StepIndex: 1, MaxSteps: 5,
		Messages: []*types.Message{types.NewUserMessage("hi")},
		State:    []byte(`{"v":1}`),
	}
}

// TestResume_KeepsCheckpointOnRestoreFailure is the C8 regression: the
// checkpoint must survive a failed resume (restore error) so the user can
// retry — it must never be consumed before the run actually succeeded.
func TestResume_KeepsCheckpointOnRestoreFailure(t *testing.T) {
	store := newMemCheckpointStore()
	_ = store.Save(context.Background(), resumeCP("cp-1"))
	r := NewRunner(&restoreFailAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

	_, err := r.Resume(context.Background(), "cp-1")
	if err == nil || !strings.Contains(err.Error(), "restore policy session") {
		t.Fatalf("Resume = %v, want restore error", err)
	}
	if !store.has("cp-1") {
		t.Fatal("checkpoint consumed on failed resume")
	}
}

// TestResume_KeepsCheckpointOnRunFailure: a restored session whose run fails
// keeps the checkpoint too.
func TestResume_KeepsCheckpointOnRunFailure(t *testing.T) {
	store := newMemCheckpointStore()
	_ = store.Save(context.Background(), resumeCP("cp-2"))
	r := NewRunner(&restoreOKRunFailAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

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
	r := NewRunner(&restoreOKAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

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
	r := NewRunner(&restoreOKAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

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
	r := NewRunner(&restoreOKAgent{}, &mockModel{}, WithRunnerCheckpointStore(store))

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
