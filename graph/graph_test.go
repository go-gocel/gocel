package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

// ── mock agent ──────────────────────────────────────────────

type mockAgent struct {
	name  string
	runFn func(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result
}

func (m *mockAgent) Name() string        { return m.name }
func (m *mockAgent) Description() string { return "mock: " + m.name }
func (m *mockAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	return m.runFn(ctx, input, rt)
}

func passthroughAgent(name, output string) *mockAgent {
	return &mockAgent{
		name: name,
		runFn: func(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			return &kernel.Result{
				Content:  output,
				Messages: []*types.Message{types.NewAssistantMessage(output)},
			}
		},
	}
}

func echoAgent(name string) *mockAgent {
	return &mockAgent{
		name: name,
		runFn: func(_ context.Context, input *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			content := fmt.Sprintf("agent_%s: %d msgs", name, len(input.Messages))
			return &kernel.Result{
				Content:  content,
				Messages: append(input.Messages, types.NewAssistantMessage(content)),
			}
		},
	}
}

// ── tests ───────────────────────────────────────────────────

func TestGraphNew(t *testing.T) {
	g := New("test")
	if g.Name() != "test" {
		t.Fatalf("expected name 'test', got %q", g.Name())
	}
}

func TestGraphAddNode(t *testing.T) {
	g := New("test")

	// single agent
	err := g.AddAgentNode("a", passthroughAgent("a", "hello"))
	if err != nil {
		t.Fatal(err)
	}

	// duplicate node
	err = g.AddAgentNode("a", passthroughAgent("a", "dup"))
	if err == nil {
		t.Fatal("expected error for duplicate node")
	}

	// chain node
	err = g.AddChainNode("chain", []kernel.Agent{
		passthroughAgent("c1", "step1"),
		passthroughAgent("c2", "step2"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// parallel node
	err = g.AddParallelNode("par", []kernel.Agent{
		passthroughAgent("p1", "par1"),
		passthroughAgent("p2", "par2"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// condition node
	err = g.AddConditionNode("cond", func(ctx ContextWithState, state *GraphState) (string, error) {
		return "yes", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// passthrough node
	err = g.AddPassthroughNode("transform", func(ctx ContextWithState, state *GraphState) error {
		state.SetWithSource("transformed", true, "transform")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGraphAddNode_Validation(t *testing.T) {
	g := New("test")

	// chain needs at least one agent
	err := g.AddChainNode("empty", []kernel.Agent{})
	if err == nil {
		t.Fatal("expected error for empty chain")
	}

	// parallel needs at least one agent
	err = g.AddParallelNode("empty", []kernel.Agent{})
	if err == nil {
		t.Fatal("expected error for empty parallel")
	}

	// condition needs non-nil fn
	err = g.AddConditionNode("nilfn", nil)
	if err == nil {
		t.Fatal("expected error for nil condition")
	}

	// passthrough needs non-nil fn
	err = g.AddPassthroughNode("nilfn", nil)
	if err == nil {
		t.Fatal("expected error for nil transform")
	}
}

func TestGraphAddEdge(t *testing.T) {
	g := New("test")
	g.AddAgentNode("a", passthroughAgent("a", ""))
	g.AddAgentNode("b", passthroughAgent("b", ""))

	err := g.AddEdge("a", "b")
	if err != nil {
		t.Fatal(err)
	}

	// non-existent source
	err = g.AddEdge("nonexistent", "b")
	if err == nil {
		t.Fatal("expected error for non-existent source")
	}

	// non-existent target
	err = g.AddEdge("a", "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent target")
	}
}

func TestGraphCompile_Linear(t *testing.T) {
	g := New("linear")
	g.AddAgentNode("a", passthroughAgent("a", "result_a"))
	g.AddAgentNode("b", passthroughAgent("b", "result_b"))
	g.AddEdge("a", "b")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cg == nil {
		t.Fatal("expected non-nil CompiledGraph")
	}
	if len(cg.topoOrder) != 2 {
		t.Fatalf("expected 2 nodes in topo order, got %d", len(cg.topoOrder))
	}
	// a must come before b
	if cg.topoOrder[0] != "a" || cg.topoOrder[1] != "b" {
		t.Fatalf("expected order [a b], got %v", cg.topoOrder)
	}
}

func TestGraphCompile_Diamond(t *testing.T) {
	//        ┌──→ B ──┐
	//   A ───┤        ├──→ D
	//        └──→ C ──┘
	g := New("diamond")
	g.AddAgentNode("a", passthroughAgent("a", ""))
	g.AddAgentNode("b", passthroughAgent("b", ""))
	g.AddAgentNode("c", passthroughAgent("c", ""))
	g.AddAgentNode("d", passthroughAgent("d", ""))
	g.AddEdge("a", "b")
	g.AddEdge("a", "c")
	g.AddEdge("b", "d")
	g.AddEdge("c", "d")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// a must be first, d must be last
	if cg.topoOrder[0] != "a" || cg.topoOrder[len(cg.topoOrder)-1] != "d" {
		t.Fatalf("unexpected order: %v", cg.topoOrder)
	}
}

func TestGraphCompile_Cycle(t *testing.T) {
	g := New("cycle")
	g.AddAgentNode("a", passthroughAgent("a", ""))
	g.AddAgentNode("b", passthroughAgent("b", ""))
	g.AddEdge("a", "b")
	g.AddEdge("b", "a") // cycle

	_, err := g.Compile(context.Background())
	if err == nil {
		t.Fatal("expected error for cycle")
	}
}

func TestGraphCompile_Empty(t *testing.T) {
	g := New("empty")
	_, err := g.Compile(context.Background())
	if err == nil {
		t.Fatal("expected error for empty graph")
	}
}

// ── execution tests ─────────────────────────────────────────

func TestRun_Linear(t *testing.T) {
	g := New("linear")
	g.AddAgentNode("a", passthroughAgent("a", "result_a"))
	g.AddAgentNode("b", passthroughAgent("b", "result_b"))
	g.AddEdge("a", "b")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	// b is last, so output should be from b
	if result.Content != "result_b" {
		t.Fatalf("expected 'result_b', got %q", result.Content)
	}
}

func TestRun_Diamond(t *testing.T) {
	//        ┌──→ B ──┐
	//   A ───┤        ├──→ D
	//        └──→ C ──┘
	g := New("diamond")
	g.AddAgentNode("a", passthroughAgent("a", "from_a"))
	g.AddAgentNode("b", passthroughAgent("b", "from_b"))
	g.AddAgentNode("c", passthroughAgent("c", "from_c"))
	g.AddAgentNode("d", passthroughAgent("d", "from_d"))
	g.AddEdge("a", "b")
	g.AddEdge("a", "c")
	g.AddEdge("b", "d")
	g.AddEdge("c", "d")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	// d is last node
	if result.Content != "from_d" {
		t.Fatalf("expected 'from_d', got %q", result.Content)
	}
}

// TestRun_NodeRetriesWrappedRetryableError (B5): a WRAPPED retryable model
// error must be retried under WithNodeMaxRetries. The old local
// retryability check only recognized a bare Retryable() interface that
// kernel.ModelError never implements — retries were silently skipped.
func TestRun_NodeRetriesWrappedRetryableError(t *testing.T) {
	var calls int
	flaky := &mockAgent{
		name: "flaky",
		runFn: func(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			calls++
			if calls <= 2 {
				return &kernel.Result{Err: fmt.Errorf("wrapped: %w", kernel.NewModelError("m", nil, true, "transient"))}
			}
			return &kernel.Result{
				Content:  "recovered",
				Messages: []*types.Message{types.NewAssistantMessage("recovered")},
			}
		},
	}
	g := New("retry")
	g.AddAgentNode("n", flaky, WithNodeMaxRetries(3))
	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatalf("Run: %v", result.Err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (two retries then success)", calls)
	}
	if result.Content != "recovered" {
		t.Fatalf("Content = %q, want recovered", result.Content)
	}
}

func TestRun_ChainNode(t *testing.T) {
	g := New("chain")
	g.AddChainNode("pipeline", []kernel.Agent{
		passthroughAgent("s1", "step1"),
		passthroughAgent("s2", "step2"),
	})
	g.AddAgentNode("final", passthroughAgent("final", "done"))
	g.AddEdge("pipeline", "final")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Content != "done" {
		t.Fatalf("expected 'done', got %q", result.Content)
	}
}

func TestRun_ParallelNode(t *testing.T) {
	g := New("parallel")
	var mu sync.Mutex
	execOrder := make([]string, 0)

	a := &mockAgent{
		name: "p1",
		runFn: func(ctx context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			execOrder = append(execOrder, "p1")
			mu.Unlock()
			return &kernel.Result{Content: "p1_res", Messages: []*types.Message{types.NewAssistantMessage("p1_res")}}
		},
	}
	b := &mockAgent{
		name: "p2",
		runFn: func(ctx context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			execOrder = append(execOrder, "p2")
			mu.Unlock()
			return &kernel.Result{Content: "p2_res", Messages: []*types.Message{types.NewAssistantMessage("p2_res")}}
		},
	}

	g.AddParallelNode("par", []kernel.Agent{a, b})
	g.AddAgentNode("final", passthroughAgent("final", "done"))
	g.AddEdge("par", "final")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}

	// p1 takes longer, but both should complete
	if len(execOrder) != 2 {
		t.Fatalf("expected 2 executions, got %v", execOrder)
	}
}

func TestRun_ConditionNode(t *testing.T) {
	g := New("branch")

	g.AddAgentNode("start", passthroughAgent("start", "begin"))
	g.AddConditionNode("router", func(ctx ContextWithState, state *GraphState) (string, error) {
		return "yes", nil
	})
	g.AddAgentNode("yes_branch", passthroughAgent("yes", "approved"))
	g.AddAgentNode("no_branch", passthroughAgent("no", "rejected"))
	g.AddAgentNode("end", passthroughAgent("end", "finished"))
	g.AddEdge("start", "router")
	g.AddLabeledEdge("router", "yes_branch", "yes")
	g.AddLabeledEdge("router", "no_branch", "no")
	g.AddEdge("yes_branch", "end")
	g.AddEdge("no_branch", "end")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Content != "finished" {
		t.Fatalf("expected 'finished', got %q", result.Content)
	}
}

// TestRun_ConditionNode_OnlyChosenBranch is a regression test: the unchosen
// branch must never execute (previously both branches ran).
func TestRun_ConditionNode_OnlyChosenBranch(t *testing.T) {
	g := New("branch-only")

	noRan := false
	g.AddAgentNode("start", passthroughAgent("start", "begin"))
	g.AddConditionNode("router", func(ctx ContextWithState, state *GraphState) (string, error) {
		return "yes", nil
	})
	g.AddAgentNode("yes_branch", passthroughAgent("yes", "approved"))
	g.AddAgentNode("no_branch", &mockAgent{
		name: "no",
		runFn: func(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			noRan = true
			return &kernel.Result{Content: "rejected"}
		},
	})
	g.AddAgentNode("end", passthroughAgent("end", "finished"))
	g.AddEdge("start", "router")
	g.AddLabeledEdge("router", "yes_branch", "yes")
	g.AddLabeledEdge("router", "no_branch", "no")
	g.AddEdge("yes_branch", "end")
	g.AddEdge("no_branch", "end")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	result := cg.Run(context.Background(), &types.AgentInput{}, runtime.NewRuntime(nil, nil))
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if noRan {
		t.Fatal("unchosen branch must not execute")
	}
	if result.Content != "finished" {
		t.Fatalf("expected join 'finished', got %q", result.Content)
	}
}

// TestRun_ConditionNode_UnmatchedBranch: a condition returning a label with no
// matching edge stops the graph with an error.
func TestRun_ConditionNode_UnmatchedBranch(t *testing.T) {
	g := New("branch-bad")

	g.AddAgentNode("start", passthroughAgent("start", "begin"))
	g.AddConditionNode("router", func(ctx ContextWithState, state *GraphState) (string, error) {
		return "maybe", nil // no edge labeled "maybe"
	})
	g.AddAgentNode("yes_branch", passthroughAgent("yes", "approved"))
	g.AddEdge("start", "router")
	g.AddLabeledEdge("router", "yes_branch", "yes")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	result := cg.Run(context.Background(), &types.AgentInput{}, runtime.NewRuntime(nil, nil))
	if result.Err == nil {
		t.Fatal("expected error for unmatched branch label")
	}
	if !strings.Contains(result.Err.Error(), "no matching edge") {
		t.Fatalf("error = %q, want 'no matching edge'", result.Err.Error())
	}
}

// TestRun_NilInput: a nil AgentInput must not panic.
func TestRun_NilInput(t *testing.T) {
	g := New("nil-input")
	g.AddAgentNode("a", passthroughAgent("a", "ok"))
	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	result := cg.Run(context.Background(), nil, runtime.NewRuntime(nil, nil))
	if result.Err != nil {
		t.Fatal(result.Err)
	}
}

// TestRun_NodeIgnoresCancellation: a node that never returns must not hang
// the graph when another node fails — runLoop must exit promptly.
func TestRun_NodeIgnoresCancellation(t *testing.T) {
	g := New("stubborn")

	g.AddAgentNode("stubborn", &mockAgent{
		name: "stubborn",
		runFn: func(ctx context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			// Deliberately ignore ctx: block until the test's outer timeout.
			select {}
		},
	})
	g.AddAgentNode("failing", &mockAgent{
		name: "failing",
		runFn: func(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			return &kernel.Result{Err: errors.New("boom")}
		},
	})

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		cg.Run(context.Background(), &types.AgentInput{}, runtime.NewRuntime(nil, nil))
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runLoop hung on a node that ignores cancellation")
	}
}

func TestRun_PassthroughNode(t *testing.T) {
	g := New("passthrough")

	g.AddAgentNode("input", passthroughAgent("input", "raw data"))
	g.AddPassthroughNode("transform", func(ctx ContextWithState, state *GraphState) error {
		if v, ok := state.Get("output"); ok {
			state.SetWithSource("output", "transformed: "+v.(string), "transform")
		}
		return nil
	})
	g.AddAgentNode("output", passthroughAgent("output", "final"))
	g.AddEdge("input", "transform")
	g.AddEdge("transform", "output")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Content != "final" {
		t.Fatalf("expected 'final', got %q", result.Content)
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	g := New("cancel")

	g.AddAgentNode("slow", &mockAgent{
		name: "slow",
		runFn: func(ctx context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			select {
			case <-time.After(5 * time.Second):
				return &kernel.Result{Content: "done"}
			case <-ctx.Done():
				return &kernel.Result{Err: ctx.Err()}
			}
		},
	})

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(ctx, &types.AgentInput{}, rt)
	if result.Err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestRun_NodeTimeout(t *testing.T) {
	g := New("timeout")

	g.AddAgentNode("fast", passthroughAgent("fast", "quick"),
		WithNodeTimeout(100*time.Millisecond),
	)

	g.AddAgentNode("slow", &mockAgent{
		name: "slow",
		runFn: func(ctx context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			select {
			case <-time.After(200 * time.Millisecond):
				return &kernel.Result{Content: "slow_done"}
			case <-ctx.Done():
				return &kernel.Result{Err: ctx.Err()}
			}
		},
	}, WithNodeTimeout(50*time.Millisecond))

	g.AddEdge("fast", "slow")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRun_AgentError(t *testing.T) {
	g := New("error")

	g.AddAgentNode("ok", passthroughAgent("ok", "fine"))
	g.AddAgentNode("fail", &mockAgent{
		name: "fail",
		runFn: func(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			return &kernel.Result{Err: errors.New("agent failed")}
		},
	})
	g.AddEdge("ok", "fail")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err == nil {
		t.Fatal("expected agent error")
	}
}

func TestRun_GraphAsAgent(t *testing.T) {
	// nest a graph inside another graph
	inner := New("inner")
	inner.AddAgentNode("ia", passthroughAgent("ia", "inner_result"))
	innerCG, err := inner.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	outer := New("outer")
	outer.AddAgentNode("inner_node", GraphAsAgent(innerCG, "nested"))
	outer.AddAgentNode("outer_final", passthroughAgent("of", "outer_done"))
	outer.AddEdge("inner_node", "outer_final")

	outerCG, err := outer.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := outerCG.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Content != "outer_done" {
		t.Fatalf("expected 'outer_done', got %q", result.Content)
	}
}

// ── state tests ─────────────────────────────────────────────

func TestGraphStateBasic(t *testing.T) {
	gs := NewGraphState()

	gs.SetWithSource("key1", "value1", "test")
	gs.SetWithSource("key2", 42, "test")

	v, ok := gs.Get("key1")
	if !ok || v.(string) != "value1" {
		t.Fatalf("expected 'value1', got %v", v)
	}

	if gs.GetInt("key2") != 42 {
		t.Fatalf("expected 42, got %d", gs.GetInt("key2"))
	}

	if gs.GetString("nonexistent") != "" {
		t.Fatal("expected empty string for nonexistent key")
	}

	if gs.Has("key1") != true {
		t.Fatal("expected Has('key1') = true")
	}

	if gs.Has("nonexistent") != false {
		t.Fatal("expected Has('nonexistent') = false")
	}
}

func TestGraphStateFork(t *testing.T) {
	parent := NewGraphState()
	parent.SetWithSource("shared", "data", "root")

	child := parent.Fork()

	// parent unaffected by child writes
	child.SetWithSource("child_only", "child_data", "fork")
	if _, ok := parent.Get("child_only"); ok {
		t.Fatal("parent should not see child's writes")
	}

	// child sees parent's data
	v, ok := child.Get("shared")
	if !ok || v.(string) != "data" {
		t.Fatal("child should see parent's data")
	}

	// parent writes don't affect child
	parent.SetWithSource("parent_only", "parent_data", "root")
	if _, ok := child.Get("parent_only"); ok {
		t.Fatal("child should not see parent's writes after fork")
	}
}

func TestGraphStateHistory(t *testing.T) {
	gs := NewGraphState()
	gs.SetWithSource("a", "1", "node1")
	gs.SetWithSource("b", "2", "node2")

	history := gs.History()
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(history))
	}

	gs.SetWithSource("a", "updated", "node1")
	history = gs.History()
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(history))
	}

	// history by source
	node1History := gs.HistoryBySource("node1")
	if len(node1History) != 2 {
		t.Fatalf("expected 2 node1 entries, got %d", len(node1History))
	}
}

func TestGraphStateRollback(t *testing.T) {
	gs := NewGraphState()
	gs.SetWithSource("a", "1", "test")
	gs.SetWithSource("b", "2", "test")
	gs.SetWithSource("a", "updated", "test")

	err := gs.Rollback(2) // keep first 2 changes
	if err != nil {
		t.Fatal(err)
	}

	v := gs.GetString("a")
	if v != "1" {
		t.Fatalf("expected '1' after rollback, got %q", v)
	}

	// rollback to 0 (empty)
	err = gs.Rollback(0)
	if err != nil {
		t.Fatal(err)
	}

	if gs.Has("a") || gs.Has("b") {
		t.Fatal("expected empty state after rollback to 0")
	}
}

func TestGraphStateSnapshot(t *testing.T) {
	gs := NewGraphState()
	gs.SetWithSource("a", "1", "test")
	gs.SetWithSource("b", "2", "test")

	snap := gs.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 keys in snapshot, got %d", len(snap))
	}

	// snapshot is isolated from subsequent writes
	gs.SetWithSource("c", "3", "test")
	if len(snap) != 2 {
		t.Fatalf("snapshot should still have 2 keys")
	}
}

func TestGraphStateDelete(t *testing.T) {
	gs := NewGraphState()
	gs.SetWithSource("a", "value", "test")
	gs.Delete("a")

	if gs.Has("a") {
		t.Fatal("expected key 'a' to be deleted")
	}

	// double delete should not panic
	gs.Delete("a")
}

func TestGraphStateClear(t *testing.T) {
	gs := NewGraphState()
	gs.SetWithSource("a", "1", "test")
	gs.SetWithSource("b", "2", "test")
	gs.Clear()

	if gs.Has("a") || gs.Has("b") {
		t.Fatal("expected empty state after clear")
	}
	if len(gs.History()) != 0 {
		t.Fatal("expected empty history after clear")
	}
}

// ── integration tests ───────────────────────────────────────

func TestRun_StatePassing(t *testing.T) {
	g := New("state_pass")

	g.AddPassthroughNode("setter", func(ctx ContextWithState, state *GraphState) error {
		state.SetWithSource("custom", "hello_from_graph", "setter")
		state.SetWithSource("count", 42, "setter")
		return nil
	})

	g.AddAgentNode("reader", &mockAgent{
		name: "reader",
		runFn: func(_ context.Context, input *types.AgentInput, _ kernel.Runtime) *kernel.Result {
			// state is not directly accessible here via AgentInput,
			// but messages are carried through
			_ = input.Messages
			return &kernel.Result{
				Content:  "read",
				Messages: []*types.Message{types.NewAssistantMessage("read")},
			}
		},
	})
	g.AddEdge("setter", "reader")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	_ = result
}

func TestRun_MultipleEntryNodes(t *testing.T) {
	//    A ──┐
	//        ├──→ C
	//    B ──┘
	g := New("multi_entry")
	g.AddAgentNode("a", passthroughAgent("a", "from_a"))
	g.AddAgentNode("b", passthroughAgent("b", "from_b"))
	g.AddAgentNode("c", passthroughAgent("c", "merged"))
	g.AddEdge("a", "c")
	g.AddEdge("b", "c")

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Content != "merged" {
		t.Fatalf("expected 'merged', got %q", result.Content)
	}
}

func TestCompile_NoEdges(t *testing.T) {
	// single node, no edges — valid for a single-agent graph
	g := New("single")
	g.AddAgentNode("only", passthroughAgent("only", "lonely"))

	cg, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cg.topoOrder) != 1 {
		t.Fatalf("expected 1 node, got %d", len(cg.topoOrder))
	}

	rt := runtime.NewRuntime(nil, nil)
	result := cg.Run(context.Background(), &types.AgentInput{}, rt)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Content != "lonely" {
		t.Fatalf("expected 'lonely', got %q", result.Content)
	}
}

func TestGraph_String(t *testing.T) {
	gs := NewGraphState()
	gs.SetWithSource("a", "hello world", "test")
	gs.SetWithSource("b", 42, "test")

	s := gs.String()
	if s == "" {
		t.Fatal("expected non-empty string")
	}
	// should contain key info
	if len(s) < 10 {
		t.Fatal("string representation too short")
	}
}

// ── StateManager interface compliance ────────────────────────

func TestGraphState_StateManagerSet(t *testing.T) {
	gs := NewGraphState()

	// kernel.StateManager.Set with 2 args (no source)
	gs.Set("key", "value")

	v, ok := gs.Get("key")
	if !ok || v != "value" {
		t.Fatalf("expected 'value', got %v", v)
	}
}

func TestGraphState_Watch(t *testing.T) {
	gs := NewGraphState()

	var mu sync.Mutex
	var received []kernel.StateChange
	cancel := gs.Watch([]string{"target"}, func(changes []kernel.StateChange) {
		mu.Lock()
		received = append(received, changes...)
		mu.Unlock()
	})

	gs.SetWithSource("target", "v1", "src1")
	gs.SetWithSource("other", "ignored", "src2")
	gs.SetWithSource("target", "v2", "src3")

	mu.Lock()
	if len(received) != 2 {
		mu.Unlock()
		t.Fatalf("expected 2 changes for 'target', got %d", len(received))
	}
	// check values
	if received[0].NewValue != "v1" || received[1].NewValue != "v2" {
		mu.Unlock()
		t.Fatalf("unexpected change values: %+v", received)
	}
	mu.Unlock()

	// cancel
	cancel()
	gs.SetWithSource("target", "v3", "src4")

	mu.Lock()
	if len(received) != 2 {
		t.Fatal("cancel should prevent further notifications")
	}
	mu.Unlock()
}

func TestGraphState_WatchAll(t *testing.T) {
	gs := NewGraphState()

	var count atomic.Int32
	gs.Watch(nil, func(changes []kernel.StateChange) {
		count.Add(int32(len(changes)))
	})

	gs.SetWithSource("a", 1, "s1")
	gs.SetWithSource("b", 2, "s2")
	gs.Delete("a")

	if c := int(count.Load()); c != 3 {
		t.Fatalf("expected 3 changes (nil keys = watch all), got %d", c)
	}
}

func TestGraphState_WatchNoMatch(t *testing.T) {
	gs := NewGraphState()

	var count atomic.Int32
	gs.Watch([]string{"monitor"}, func(changes []kernel.StateChange) {
		count.Add(int32(len(changes)))
	})

	gs.SetWithSource("other", 1, "s1")

	if c := int(count.Load()); c != 0 {
		t.Fatalf("expected 0 changes, got %d", c)
	}
}

func TestGraphState_DeleteNotifiesWatcher(t *testing.T) {
	gs := NewGraphState()

	var count atomic.Int32
	gs.Watch(nil, func(changes []kernel.StateChange) {
		count.Add(int32(len(changes)))
	})

	gs.SetWithSource("key", "val", "s1")
	gs.Delete("key")

	if c := int(count.Load()); c != 2 {
		t.Fatalf("expected 2 changes (set+delete), got %d", c)
	}
}

func TestGraphState_StateManagerSnapshot(t *testing.T) {
	gs := NewGraphState()

	// Set via both SetWithSource and StateManager.Set
	gs.SetWithSource("a", "from_source", "src")
	gs.Set("b", "from_manager")

	snap := gs.Snapshot()
	if snap["a"] != "from_source" || snap["b"] != "from_manager" {
		t.Fatalf("unexpected snapshot: %v", snap)
	}
}
