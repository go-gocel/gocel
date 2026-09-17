package goalround

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/goal"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

type fakeRegistrar struct {
	start kernel.AgentStartHook
	msgs  kernel.MessagesHook
	end   kernel.AgentEndHook
}

func (r *fakeRegistrar) OnAgentStart(fn kernel.AgentStartHook) func()       { r.start = fn; return func() {} }
func (r *fakeRegistrar) OnAgentEnd(fn kernel.AgentEndHook) func()           { r.end = fn; return func() {} }
func (r *fakeRegistrar) OnMessagesBuilt(fn kernel.MessagesHook) func()      { r.msgs = fn; return func() {} }
func (r *fakeRegistrar) OnStepStart(fn kernel.StepHook) func()              { return nil }
func (r *fakeRegistrar) OnStepEnd(fn kernel.StepHook) func()                { return nil }
func (r *fakeRegistrar) OnModelCall(fn kernel.ModelCallHook) func()         { return nil }
func (r *fakeRegistrar) OnModelResult(fn kernel.ModelResultHookFunc) func() { return nil }
func (r *fakeRegistrar) OnToolCall(fn kernel.ToolCallHook) func()           { return nil }
func (r *fakeRegistrar) OnToolResult(fn kernel.ToolResultHookFunc) func()   { return nil }
func (r *fakeRegistrar) OnDecision(fn kernel.DecisionHook) func()           { return nil }

type fixture struct {
	m       *Module
	mgr     *goal.Manager
	reg     *fakeRegistrar
	ac      *runtime.AgentContext
	ctx     context.Context
	notices []*types.Notice
	mu      sync.Mutex
}

func newFixture(t *testing.T, sessionID string, maxRounds int) *fixture {
	t.Helper()
	mgr := goal.NewManager(goal.NewMemoryStore())
	m := New(mgr)
	reg := &fakeRegistrar{}
	m.Register(reg)

	ac := runtime.NewAgentContext(
		runtime.WithContextFacts(&types.RuntimeFacts{SessionID: sessionID}),
	)
	f := &fixture{m: m, mgr: mgr, reg: reg, ac: ac}
	ac.SetSendEvent(func(ev *types.Event) bool {
		if ev.Notice != nil {
			f.mu.Lock()
			f.notices = append(f.notices, ev.Notice)
			f.mu.Unlock()
		}
		return true
	})
	f.ctx = kernel.WithAgentContext(context.Background(), ac)
	return f
}

func (f *fixture) startRun(t *testing.T) {
	t.Helper()
	ctx, _, err := f.reg.start(f.ctx, &kernel.AgentRunInfo{})
	if err != nil {
		t.Fatalf("AgentStart = %v", err)
	}
	f.ctx = ctx
}

func (f *fixture) endRun(t *testing.T) {
	t.Helper()
	ctx, _, err := f.reg.end(f.ctx, &kernel.RunInfo{})
	if err != nil {
		t.Fatalf("AgentEnd = %v", err)
	}
	f.ctx = ctx
}

func (f *fixture) hostTurn() bool {
	v, ok := f.ac.State().Get(HostTurnKey)
	b, _ := v.(bool)
	return ok && b
}

func TestGoalround_HostRoundThenContinuation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "s1", 0)
	g, _ := f.mgr.Create(ctx, "ship the release", 0)
	f.m.Arm("s1", g.ID)

	// First run: host turn, round 1 claimed, goal block injected.
	f.startRun(t)
	if !f.hostTurn() {
		t.Fatal("first run must be a host turn")
	}
	got, _ := f.mgr.Get(ctx, g.ID)
	if got.Rounds != 1 {
		t.Fatalf("rounds after first start = %d, want 1", got.Rounds)
	}
	_, msgs, err := f.reg.msgs(f.ctx, []*types.Message{types.NewUserMessage("hi")})
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages = %d msgs, %v; want injected goal block", len(msgs), err)
	}
	if !strings.Contains(msgs[1].Content, "## Goal (round 1") || !strings.Contains(msgs[1].Content, "ship the release") {
		t.Fatalf("goal block = %q, want round 1 + objective", msgs[1].Content)
	}

	// Goal still active → continuation available.
	if goalID, ok := f.m.ShouldContinue(ctx, "s1"); !ok || goalID != g.ID {
		t.Fatalf("ShouldContinue = %q, %v; want %q, true", goalID, ok, g.ID)
	}
	f.endRun(t)

	// Second run: goal round (not host), round 2 claimed.
	f.startRun(t)
	if f.hostTurn() {
		t.Fatal("continuation run must not be a host turn")
	}
	got, _ = f.mgr.Get(ctx, g.ID)
	if got.Rounds != 2 {
		t.Fatalf("rounds after second start = %d, want 2", got.Rounds)
	}
}

func TestGoalround_SettlesWithNotice(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "s1", 0)
	g, _ := f.mgr.Create(ctx, "objective", 0)
	f.m.Arm("s1", g.ID)

	f.startRun(t)
	_, _ = f.mgr.Complete(ctx, g.ID)
	f.endRun(t)

	if _, ok := f.m.ShouldContinue(ctx, "s1"); ok {
		t.Fatal("completed goal must not continue")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.notices) != 1 {
		t.Fatalf("notices = %d, want 1", len(f.notices))
	}
	n := f.notices[0]
	if n.Kind != types.NoticeKindGoalRound || n.ID != g.ID || n.Status != "complete" {
		t.Fatalf("notice = %+v, want goal_round/complete", n)
	}
}

func TestGoalround_ClaimFailureDisarms(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "s1", 1)
	g, _ := f.mgr.Create(ctx, "objective", 1)
	f.m.Arm("s1", g.ID)

	f.startRun(t) // claims round 1 (the cap)
	f.endRun(t)   // cap exhausted: settles with rounds_exhausted + disarms
	got, _ := f.mgr.Get(ctx, g.ID)
	if got.Rounds != 1 {
		t.Fatalf("rounds = %d, want 1", got.Rounds)
	}
	f.mu.Lock()
	if len(f.notices) != 1 || f.notices[0].Status != "rounds_exhausted" {
		f.mu.Unlock()
		t.Fatalf("notices = %+v, want one rounds_exhausted notice", f.notices)
	}
	f.mu.Unlock()
	if _, ok := f.m.ShouldContinue(ctx, "s1"); ok {
		t.Fatal("goal must be disarmed after cap exhaustion")
	}
	// A further run is an ordinary host run: no claim, no extra notice.
	f.startRun(t)
	got, _ = f.mgr.Get(ctx, g.ID)
	if got.Rounds != 1 {
		t.Fatalf("rounds after plain run = %d, want 1 (no over-claim)", got.Rounds)
	}
}

func TestGoalround_BlockedSettlesWithReason(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "s1", 0)
	g, _ := f.mgr.Create(ctx, "objective", 0)
	f.m.Arm("s1", g.ID)

	f.startRun(t)
	_, _ = f.mgr.Block(ctx, g.ID, "env missing")
	f.endRun(t)

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.notices) != 1 || f.notices[0].Status != "blocked" || f.notices[0].Label != "env missing" {
		t.Fatalf("notices = %+v, want blocked with reason", f.notices)
	}
}

func TestGoalround_NoSessionIDStaysInert(t *testing.T) {
	f := newFixture(t, "", 0)
	f.startRun(t)
	if _, ok := f.ac.State().Get(HostTurnKey); ok {
		t.Fatal("without a session id the driver must stay inert")
	}
}

func TestGoalround_DisarmStopsRounds(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "s1", 0)
	g, _ := f.mgr.Create(ctx, "objective", 0)
	f.m.Arm("s1", g.ID)
	f.m.Disarm("s1")
	if _, ok := f.m.ShouldContinue(ctx, "s1"); ok {
		t.Fatal("disarmed goal must not continue")
	}
}
