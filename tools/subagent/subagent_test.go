package subagent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/module/permission"
)

// recAgent records every input it runs.
type recAgent struct {
	mu     sync.Mutex
	inputs [][]*types.Message
}

func (a *recAgent) Name() string        { return "rec" }
func (a *recAgent) Description() string { return "rec" }
func (a *recAgent) Run(_ context.Context, input *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	a.mu.Lock()
	a.inputs = append(a.inputs, input.Messages)
	a.mu.Unlock()
	return &kernel.Result{Content: "done"}
}

func (a *recAgent) last() []*types.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.inputs) == 0 {
		return nil
	}
	return a.inputs[len(a.inputs)-1]
}

func toolCtx() context.Context {
	rt := runtime.NewRuntime(nil, nil)
	ac := runtime.NewAgentContext(runtime.WithContextFacts(&types.RuntimeFacts{SessionID: "s1"}))
	ctx := kernel.WithRuntime(context.Background(), rt)
	return kernel.WithAgentContext(ctx, ac)
}

func TestSpawnTool_ReturnsHandleAndRuns(t *testing.T) {
	reg := orchestrate.NewRegistry()
	child := &recAgent{}
	ts, err := Tools(Config{Registry: reg, Factory: func(context.Context) kernel.Agent { return child }})
	if err != nil {
		t.Fatal(err)
	}
	spawn := ts[0]

	out, err := spawn.Run(toolCtx(), `{"task":"research X","label":"r"}`)
	if err != nil {
		t.Fatalf("subagent tool = %v", err)
	}
	var sub struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal([]byte(out), &sub); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if sub.ID == "" || sub.Label != "r" {
		t.Fatalf("handle = %+v, want id and label", sub)
	}

	// The child runs the task on the parent runtime (background).
	deadline := time.After(3 * time.Second)
	for child.last() == nil {
		select {
		case <-deadline:
			t.Fatal("child never ran")
		case <-time.After(5 * time.Millisecond):
		}
	}
	msgs := child.last()
	if len(msgs) != 1 || msgs[0].Content != "research X" {
		t.Fatalf("child input = %+v, want the task", msgs)
	}
	live := reg.List(context.Background())
	if len(live) != 1 || live[0].ID != sub.ID {
		t.Fatalf("live = %+v, want the spawned child", live)
	}
}

func TestForkTool_InheritsHistory(t *testing.T) {
	reg := orchestrate.NewRegistry()
	child := &recAgent{}
	history := []*types.Message{
		types.NewUserMessage("earlier turn"),
		types.NewAssistantMessage("earlier answer"),
	}
	ts, err := Tools(Config{
		Registry: reg,
		Factory:  func(context.Context) kernel.Agent { return child },
		History:  func(context.Context) []*types.Message { return history },
	})
	if err != nil {
		t.Fatal(err)
	}
	fork := ts[1]

	if _, err := fork.Run(toolCtx(), `{"task":"branch out","label":"f"}`); err != nil {
		t.Fatalf("subagent_fork = %v", err)
	}
	deadline := time.After(3 * time.Second)
	for child.last() == nil {
		select {
		case <-deadline:
			t.Fatal("forked child never ran")
		case <-time.After(5 * time.Millisecond):
		}
	}
	msgs := child.last()
	if len(msgs) != 3 || msgs[0].Content != "earlier turn" || msgs[2].Content != "branch out" {
		t.Fatalf("fork input = %+v, want history + task", msgs)
	}
}

func TestSpawnTool_RequiresTask(t *testing.T) {
	reg := orchestrate.NewRegistry()
	ts, err := Tools(Config{Registry: reg, Factory: func(context.Context) kernel.Agent { return &recAgent{} }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts[0].Run(toolCtx(), `{"task":""}`); err == nil {
		t.Fatal("empty task = nil, want error")
	}
}

// TestSpawnTool_DepthLimit pins the delegation depth floor: a spawn from a
// caller at or beyond MaxDepth is rejected.
func TestSpawnTool_DepthLimit(t *testing.T) {
	reg := orchestrate.NewRegistry()
	ts, err := Tools(Config{
		Registry: reg,
		Factory:  func(context.Context) kernel.Agent { return &recAgent{} },
		MaxDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	deep := WithDepth(toolCtx(), 2)
	if _, err := ts[0].Run(deep, `{"task":"too deep"}`); err == nil {
		t.Fatal("spawn beyond MaxDepth = nil, want depth-limit error")
	}
	if _, err := ts[0].Run(WithDepth(toolCtx(), 1), `{"task":"ok","label":"l"}`); err != nil {
		t.Fatalf("spawn within depth = %v, want nil", err)
	}
}

// policyAgent records the delegation policy its run context carries and
// signals each run.
type policyAgent struct {
	mu      sync.Mutex
	policy  kernel.ApprovalPolicy
	runs    int
	started chan struct{}
}

func (a *policyAgent) Name() string        { return "policy" }
func (a *policyAgent) Description() string { return "policy" }
func (a *policyAgent) Run(ctx context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	a.mu.Lock()
	a.policy = kernel.DelegatedApprovalFromContext(ctx)
	a.runs++
	a.mu.Unlock()
	select {
	case a.started <- struct{}{}:
	default:
	}
	return &kernel.Result{Content: "ok"}
}

func (a *policyAgent) lastPolicy() kernel.ApprovalPolicy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.policy
}

// waitPolicyRun waits for the child's first run.
func waitPolicyRun(t *testing.T, started chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("child never ran")
	}
}

// TestSpawnTool_DelegatedApprovalPinned: the Config-level pinned approval
// policy is captured at the delegation boundary and reaches the child's run
// context; an unpinned config carries nothing.
func TestSpawnTool_DelegatedApprovalPinned(t *testing.T) {
	reg := orchestrate.NewRegistry()
	child := &policyAgent{started: make(chan struct{}, 1)}
	ts, err := Tools(Config{
		Registry:          reg,
		Factory:           func(context.Context) kernel.Agent { return child },
		DelegatedApproval: permission.NeverApprovalPolicy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts[0].Run(toolCtx(), `{"task":"run","label":"p"}`); err != nil {
		t.Fatalf("spawn = %v", err)
	}
	waitPolicyRun(t, child.started)
	if _, ok := child.lastPolicy().(permission.NeverApprovalPolicy); !ok {
		t.Fatalf("child policy = %T, want permission.NeverApprovalPolicy", child.lastPolicy())
	}
}

// TestSpawnTool_NoDelegatedApprovalByDefault: the zero config pins nothing —
// the child resolves its own approval disposition.
func TestSpawnTool_NoDelegatedApprovalByDefault(t *testing.T) {
	reg := orchestrate.NewRegistry()
	child := &policyAgent{started: make(chan struct{}, 1)}
	ts, err := Tools(Config{
		Registry: reg,
		Factory:  func(context.Context) kernel.Agent { return child },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts[0].Run(toolCtx(), `{"task":"run","label":"u"}`); err != nil {
		t.Fatalf("spawn = %v", err)
	}
	waitPolicyRun(t, child.started)
	if p := child.lastPolicy(); p != nil {
		t.Fatalf("unpinned child policy = %T, want nil", p)
	}
}
