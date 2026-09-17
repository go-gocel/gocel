package graph

import (
	"context"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// recordingAgent records whether it ran and what it produced.
type recordingAgent struct {
	name    string
	ran     *bool
	content string
}

func (a *recordingAgent) Name() string        { return a.name }
func (a *recordingAgent) Description() string { return "recording" }
func (a *recordingAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	if a.ran != nil {
		*a.ran = true
	}
	return &kernel.Result{Content: a.content, Messages: input.Messages}
}

// Regression (empirically verified defect): an unchosen condition branch
// used to skip a JOIN node that still had a live non-condition predecessor
// — the join and its whole downstream subtree silently never ran.
//
// Graph: A→B→D→F, A→C(condition)→D("x"), C→E("y"). Condition picks "y":
// D is still reachable through B and must run.
func TestGraph_ConditionJoinWithLivePredecessorRuns(t *testing.T) {
	bRan, dRan, fRan, eRan := false, false, false, false
	g := New("t")
	must(t, g.AddPassthroughNode("a", func(ctx context.Context, st *GraphState) error {
		st.Set("messages", []*types.Message{types.NewUserMessage("go")})
		return nil
	}))
	must(t, g.AddAgentNode("b", &recordingAgent{name: "b", ran: &bRan, content: "B-out"}))
	must(t, g.AddAgentNode("d", &recordingAgent{name: "d", ran: &dRan, content: "D-out"}))
	must(t, g.AddAgentNode("f", &recordingAgent{name: "f", ran: &fRan, content: "F-out"}))
	must(t, g.AddAgentNode("e", &recordingAgent{name: "e", ran: &eRan, content: "E-out"}))
	must(t, g.AddConditionNode("c", func(ctx context.Context, st *GraphState) (string, error) {
		return "y", nil
	}))
	must(t, g.AddEdge("a", "b"))
	must(t, g.AddEdge("a", "c"))
	must(t, g.AddLabeledEdge("c", "d", "x"))
	must(t, g.AddLabeledEdge("c", "e", "y"))
	must(t, g.AddEdge("b", "d"))
	must(t, g.AddEdge("d", "f"))

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res := cg.Run(context.Background(), &types.AgentInput{}, nil)
	if res.Err != nil {
		t.Fatalf("graph run: %v", res.Err)
	}
	if !bRan || !eRan {
		t.Fatalf("b=%v e=%v — both branches must run", bRan, eRan)
	}
	if !dRan || !fRan {
		t.Fatalf("d=%v f=%v — the join node D and its downstream F were silently skipped", dRan, fRan)
	}
}

// Regression: state watchers used to run under the write lock — a watcher
// reading state deadlocked. Callbacks must run outside the lock.
func TestGraphState_WatcherReentrantReadNoDeadlock(t *testing.T) {
	gs := NewGraphState()
	done := make(chan struct{})
	gs.Watch([]string{"k"}, func(changes []kernel.StateChange) {
		// Re-entrant read inside the callback would deadlock if the
		// callback ran under the write lock.
		gs.Get("k")
		close(done)
	})
	gs.Set("k", 1)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher deadlocked (callback ran under the write lock)")
	}
}

// Regression: merged keys must flow through history + watchers (they were
// written directly into data, invisible to Rollback and observers).
func TestGraphState_MergeFromNotifiesWatchers(t *testing.T) {
	src := NewGraphState()
	src.Set("k", "v")
	dst := NewGraphState()
	notified := make(chan struct{}, 1)
	dst.Watch([]string{"k"}, func(changes []kernel.StateChange) {
		notified <- struct{}{}
	})
	if err := dst.MergeFrom(src); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	default:
		t.Fatal("MergeFrom did not notify watchers")
	}
	// And the merge is visible to history (rollback).
	if len(dst.History()) == 0 {
		t.Fatal("MergeFrom did not record history")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
