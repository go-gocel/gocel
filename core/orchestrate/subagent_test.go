package orchestrate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// stubRuntime satisfies kernel.Runtime for tests without importing
// core/runtime (which imports orchestrate — a cycle).
type stubRuntime struct{}

func (s *stubRuntime) CallModel(context.Context, []*types.Message, ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return nil, nil, errors.New("unused")
}
func (s *stubRuntime) CallModelStream(context.Context, []*types.Message, ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("unused")
}
func (s *stubRuntime) ExecTools(context.Context, []*types.ToolCall) []*types.Message { return nil }
func (s *stubRuntime) CountTokens(context.Context, []*types.Message, ...kernel.GenOption) (int, error) {
	return 0, nil
}
func (s *stubRuntime) ListTools(context.Context) []kernel.Tool { return nil }
func (s *stubRuntime) Register(context.Context, kernel.ToolProvider) error {
	return nil
}
func (s *stubRuntime) Unregister(context.Context, string) error { return nil }
func (s *stubRuntime) State() kernel.StateManager               { return nil }
func (s *stubRuntime) FireAgentStart(ctx context.Context, info *kernel.AgentRunInfo) (context.Context, *kernel.AgentRunInfo, error) {
	return ctx, info, nil
}
func (s *stubRuntime) FireAgentEnd(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
	return ctx, info, nil
}
func (s *stubRuntime) FireMessagesBuilt(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
	return ctx, msgs, nil
}
func (s *stubRuntime) FireStepStart(ctx context.Context, info *kernel.StepInfo) (context.Context, bool, *kernel.StepInfo, error) {
	return ctx, true, info, nil
}
func (s *stubRuntime) FireStepEnd(ctx context.Context, info *kernel.StepInfo) (context.Context, bool, *kernel.StepInfo, error) {
	return ctx, true, info, nil
}
func (s *stubRuntime) FireDecision(ctx context.Context, info *kernel.DecisionInfo) error {
	return nil
}

// fakeAgentCtx implements kernel.AgentContext for tests: only the event
// sender matters to the registry.
type fakeAgentCtx struct {
	send func(*types.Event) bool
}

func (a *fakeAgentCtx) InvocationID() string                    { return "t" }
func (a *fakeAgentCtx) AgentName() string                       { return "t" }
func (a *fakeAgentCtx) Branch() string                          { return "" }
func (a *fakeAgentCtx) RunPath() string                         { return "" }
func (a *fakeAgentCtx) ContextPassing() string                  { return "" }
func (a *fakeAgentCtx) EnableStreaming() bool                   { return false }
func (a *fakeAgentCtx) InterruptInput() chan string             { return nil }
func (a *fakeAgentCtx) SendEvent() func(*types.Event) bool      { return a.send }
func (a *fakeAgentCtx) ParentAgent() kernel.Agent               { return nil }
func (a *fakeAgentCtx) State() kernel.StateManager              { return nil }
func (a *fakeAgentCtx) Facts() *types.RuntimeFacts              { return &types.RuntimeFacts{} }
func (a *fakeAgentCtx) SetSendEvent(fn func(*types.Event) bool) {}
func (a *fakeAgentCtx) SetInterruptInput(ch chan string)        {}

// scriptAgent records every turn's first message and optionally blocks
// until released, so tests control turn timing precisely.
type scriptAgent struct {
	name  string
	fail  bool
	block chan struct{} // when non-nil, each turn blocks until closed or ctx done

	mu      sync.Mutex
	turns   []string
	started chan string
}

func (a *scriptAgent) Name() string        { return a.name }
func (a *scriptAgent) Description() string { return "scripted" }
func (a *scriptAgent) Run(ctx context.Context, input *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	content := ""
	if len(input.Messages) > 0 {
		content = input.Messages[0].Content
	}
	a.mu.Lock()
	a.turns = append(a.turns, content)
	a.mu.Unlock()
	if a.started != nil {
		a.started <- content
	}
	if a.block != nil {
		select {
		case <-a.block:
		case <-ctx.Done():
			return &kernel.Result{Err: ctx.Err()}
		}
	}
	if a.fail {
		return &kernel.Result{Err: errors.New("scripted failure")}
	}
	return &kernel.Result{Content: "done:" + content}
}

func (a *scriptAgent) turnList() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.turns...)
}

// fixture wires a registry with a parent context carrying a runtime and an
// event-capturing AgentContext.
type fixture struct {
	reg     *Registry
	ctx     context.Context
	mu      sync.Mutex
	notices []*types.Notice
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	reg := NewRegistry()
	ac := &fakeAgentCtx{}
	ctx := kernel.WithRuntime(context.Background(), &stubRuntime{})
	ctx = kernel.WithAgentContext(ctx, ac)
	f := &fixture{reg: reg, ctx: ctx}
	ac.send = func(ev *types.Event) bool {
		if ev.Notice != nil {
			f.mu.Lock()
			f.notices = append(f.notices, ev.Notice)
			f.mu.Unlock()
		}
		return true
	}
	return f
}

func (f *fixture) noticesByStatus() map[string][]*types.Notice {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]*types.Notice{}
	for _, n := range f.notices {
		out[n.Status] = append(out[n.Status], n)
	}
	return out
}

func waitFor(t *testing.T, ch chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("turn started with %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn never started")
	}
}

func TestSubagent_SpawnBackgroundThenContinue(t *testing.T) {
	f := newFixture(t)
	a := &scriptAgent{name: "child", started: make(chan string, 4)}
	sub, err := f.reg.Spawn(f.ctx, "", "research", a, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("first")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub.ID == "" || sub.Status != StatusRunning {
		t.Fatalf("sub = %+v, want running with id", sub)
	}
	waitFor(t, a.started, "first")

	// Continue while the first turn may still run: the message queues and
	// runs after the current turn (DSH send_message wait semantics).
	if err := f.reg.Continue(context.Background(), sub.ID, "second"); err != nil {
		t.Fatalf("Continue = %v", err)
	}
	waitFor(t, a.started, "second")

	// Give the idle transition a moment, then verify the turn list.
	deadline := time.After(5 * time.Second)
	for {
		if len(a.turnList()) == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("turns = %v, want [first second]", a.turnList())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := a.turnList(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("turns = %v, want [first second]", got)
	}
	// Wait must block on a live session (it settles only on failure/dispose).
	probeCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := f.reg.Wait(probeCtx, sub.ID); err == nil {
		t.Fatal("Wait returned on a live session — it must block until settle")
	}
}

func TestSubagent_InterruptStopsCurrentTurnOnly(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	a := &scriptAgent{name: "child", block: release, started: make(chan string, 4)}
	sub, _ := f.reg.Spawn(f.ctx, "", "t", a, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("long")},
	})
	waitFor(t, a.started, "long")

	if err := f.reg.Interrupt(context.Background(), sub.ID); err != nil {
		t.Fatalf("Interrupt = %v", err)
	}
	// The session must stay continuable after the interrupt.
	if err := f.reg.Continue(context.Background(), sub.ID, "next"); err != nil {
		t.Fatalf("Continue after Interrupt = %v, want nil (session stays live)", err)
	}
	close(release) // the interrupted turn unblocks; "next" runs after
	waitFor(t, a.started, "next")
}

func TestSubagent_FailureSettlesWithNotice(t *testing.T) {
	f := newFixture(t)
	a := &scriptAgent{name: "child", fail: true, started: make(chan string, 4)}
	sub, _ := f.reg.Spawn(f.ctx, "", "boom", a, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("go")},
	})

	res, err := f.reg.Wait(context.Background(), sub.ID)
	if err != nil || res == nil || res.Err == nil {
		t.Fatalf("Wait = %+v, %v; want failed result", res, err)
	}
	if err := f.reg.Continue(context.Background(), sub.ID, "more"); !errors.Is(err, ErrSubagentNotContinuable) {
		t.Fatalf("Continue after failure = %v, want ErrSubagentNotContinuable", err)
	}
	if live := f.reg.List(context.Background()); len(live) != 0 {
		t.Fatalf("List after failure = %v, want empty (live agents only)", live)
	}
	ns := f.noticesByStatus()
	if len(ns["failed"]) != 1 || ns["failed"][0].ID != sub.ID || ns["failed"][0].Err == "" {
		t.Fatalf("failed notices = %+v, want one with id and error", ns)
	}
}

func TestSubagent_DisposeSettlesAndRemoves(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	a := &scriptAgent{name: "child", block: release, started: make(chan string, 4)}
	sub, _ := f.reg.Spawn(f.ctx, "", "t", a, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("blocked")},
	})
	waitFor(t, a.started, "blocked")

	if err := f.reg.Dispose(context.Background(), sub.ID); err != nil {
		t.Fatalf("Dispose = %v", err)
	}
	if err := f.reg.Continue(context.Background(), sub.ID, "more"); !errors.Is(err, ErrSubagentNotFound) {
		t.Fatalf("Continue after Dispose = %v, want ErrSubagentNotFound", err)
	}
	ns := f.noticesByStatus()
	if len(ns["disposed"]) != 1 {
		t.Fatalf("disposed notices = %+v, want one", ns)
	}
}

func TestSubagent_Descendants(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	mk := func(parent string) *Subagent {
		a := &scriptAgent{name: "c", block: release, started: make(chan string, 4)}
		sub, err := f.reg.Spawn(f.ctx, parent, "c", a, &types.AgentInput{
			Messages: []*types.Message{types.NewUserMessage("hold")},
		})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, a.started, "hold")
		return sub
	}
	p1a := mk("p1")
	p1b := mk("p1")
	mk("p2")

	kids := f.reg.Descendants(context.Background(), "p1")
	byID := map[string]bool{}
	for _, k := range kids {
		byID[k.ID] = true
	}
	if len(kids) != 2 || !byID[p1a.ID] || !byID[p1b.ID] {
		t.Fatalf("Descendants(p1) = %+v, want the two p1 children (%s, %s)", kids, p1a.ID, p1b.ID)
	}
	if roots := f.reg.Descendants(context.Background(), ""); len(roots) != 0 {
		t.Fatalf("Descendants(\"\") = %v, want none (all children have parents)", roots)
	}
}

// ── one-shot Run ────────────────────────────────────────────────────────

// TestRegistryRun_OneShotSuccess proves the one-shot channel: exactly one
// turn, the result comes back synchronously, the session settles as
// completed (notice), and the registry forgets it.
func TestRegistryRun_OneShotSuccess(t *testing.T) {
	f := newFixture(t)
	a := &scriptAgent{name: "child", started: make(chan string, 4)}
	result, err := f.reg.Run(f.ctx, "", "item-1", a, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("task")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Content != "done:task" {
		t.Fatalf("result = %+v, want the child's content", result)
	}
	if got := len(f.reg.List(context.Background())); got != 0 {
		t.Fatalf("one-shot sessions must leave the registry, List has %d", got)
	}
	ns := f.noticesByStatus()
	if len(ns[string(StatusCompleted)]) != 1 {
		t.Fatalf("completed notices = %+v, want one", ns)
	}
}

// TestRegistryRun_FailureReturnsResultErr proves a failing turn surfaces as
// the child's Result.Err (the caller decides how to degrade).
func TestRegistryRun_FailureReturnsResultErr(t *testing.T) {
	f := newFixture(t)
	a := &scriptAgent{name: "child", fail: true, started: make(chan string, 4)}
	result, err := f.reg.Run(f.ctx, "", "item-2", a, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("task")},
	})
	if err != nil {
		t.Fatalf("Run must not error itself: %v", err)
	}
	if result == nil || result.Err == nil {
		t.Fatal("a failing turn must return its Result with Err set")
	}
	ns := f.noticesByStatus()
	if len(ns[string(StatusFailed)]) != 1 {
		t.Fatalf("failed notices = %+v, want one", ns)
	}
}

// TestRegistryRun_CancelDisposesChild proves cancellation: a canceled ctx
// cancels the child turn, disposes the session, and returns the ctx error.
func TestRegistryRun_CancelDisposesChild(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	a := &scriptAgent{name: "child", block: release, started: make(chan string, 4)}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() {
		_, err := f.reg.Run(ctx, "", "item-3", a, &types.AgentInput{
			Messages: []*types.Message{types.NewUserMessage("slow")},
		})
		done <- err
	}()
	waitFor(t, a.started, "slow")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run on canceled ctx = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if got := len(f.reg.List(context.Background())); got != 0 {
		t.Fatalf("canceled one-shot must leave the registry, List has %d", got)
	}
}

// TestRegistryRun_RequiresParentRuntime proves the same contract as Spawn:
// the parent runtime must be in the context.
func TestRegistryRun_RequiresParentRuntime(t *testing.T) {
	reg := NewRegistry()
	a := &scriptAgent{name: "child"}
	if _, err := reg.Run(context.Background(), "", "x", a, &types.AgentInput{}); err == nil {
		t.Fatal("Run without a runtime in context must fail")
	}
}

// neverPolicy denies every call — the delegation-fixed disposition.
type neverPolicy struct{}

func (neverPolicy) Decide(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
	return kernel.ApprovalDeny, nil
}

// delegatingAgent records the approval policy its run context carries and
// signals each run through the started channel.
type delegatingAgent struct {
	mu      sync.Mutex
	policy  kernel.ApprovalPolicy
	started chan struct{}
}

func (a *delegatingAgent) Name() string        { return "delegating" }
func (a *delegatingAgent) Description() string { return "delegating" }
func (a *delegatingAgent) Run(ctx context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	a.mu.Lock()
	a.policy = kernel.DelegatedApprovalFromContext(ctx)
	a.mu.Unlock()
	select {
	case a.started <- struct{}{}:
	default:
	}
	return &kernel.Result{Content: "ok"}
}

func (a *delegatingAgent) lastPolicy() kernel.ApprovalPolicy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.policy
}

// waitStarted waits for the child's first run signal.
func waitStarted(t *testing.T, started chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("child never ran")
	}
}

// TestSpawn_DelegatedApprovalPinned proves the delegation-policy law: the
// approval policy pinned at the spawn boundary reaches the child's run
// context, and an unpinned spawn carries nil (the child resolves its own).
func TestSpawn_DelegatedApprovalPinned(t *testing.T) {
	f := newFixture(t)
	child := &delegatingAgent{started: make(chan struct{}, 1)}

	// Pinned: the policy travels into the child context.
	pinned := kernel.WithDelegatedApproval(f.ctx, neverPolicy{})
	sub, err := f.reg.Spawn(pinned, "s1", "pinned", child, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("task")},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, child.started)
	if _, ok := child.lastPolicy().(neverPolicy); !ok {
		t.Fatalf("child policy = %T, want neverPolicy", child.lastPolicy())
	}
	_ = f.reg.Dispose(context.Background(), sub.ID)

	// Unpinned: no delegation policy reaches the child.
	child2 := &delegatingAgent{started: make(chan struct{}, 1)}
	sub2, err := f.reg.Spawn(f.ctx, "s1", "unpinned", child2, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("task")},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, child2.started)
	if p := child2.lastPolicy(); p != nil {
		t.Fatalf("unpinned child policy = %T, want nil", p)
	}
	_ = f.reg.Dispose(context.Background(), sub2.ID)
}
