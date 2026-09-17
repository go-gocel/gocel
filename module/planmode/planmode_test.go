package planmode

import (
	"context"
	"errors"
	"testing"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ── mocks ───────────────────────────────────────────────────────────────

type mockRegistrar struct {
	toolCall func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error)
}

func (r *mockRegistrar) OnAgentStart(fn kernel.AgentStartHook) func()       { return nil }
func (r *mockRegistrar) OnAgentEnd(fn kernel.AgentEndHook) func()           { return nil }
func (r *mockRegistrar) OnMessagesBuilt(fn kernel.MessagesHook) func()      { return nil }
func (r *mockRegistrar) OnStepStart(fn kernel.StepHook) func()              { return nil }
func (r *mockRegistrar) OnStepEnd(fn kernel.StepHook) func()                { return nil }
func (r *mockRegistrar) OnModelCall(fn kernel.ModelCallHook) func()         { return nil }
func (r *mockRegistrar) OnModelResult(fn kernel.ModelResultHookFunc) func() { return nil }
func (r *mockRegistrar) OnToolCall(fn kernel.ToolCallHook) func() {
	r.toolCall = fn
	return func() {}
}
func (r *mockRegistrar) OnToolResult(fn kernel.ToolResultHookFunc) func() { return nil }
func (r *mockRegistrar) OnDecision(fn kernel.DecisionHook) func()         { return nil }

type gateFixture struct {
	mode     types.SessionMode
	decision kernel.ApprovalDecision
	askErr   error
	approved []string
	askCalls int
}

func (f *gateFixture) modeFn() types.SessionMode { return f.mode }
func (f *gateFixture) ask(_ context.Context, _ *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
	f.askCalls++
	return f.decision, f.askErr
}
func (f *gateFixture) approve(plan string) { f.approved = append(f.approved, plan) }

func newModule(t *testing.T, f *gateFixture) *Module {
	t.Helper()
	m, err := New(Config{Mode: f.modeFn, Ask: f.ask, OnApprove: f.approve})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func gateOf(m *Module) func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	r := &mockRegistrar{}
	m.Register(r)
	return r.toolCall
}

// ── gate ────────────────────────────────────────────────────────────────

func TestGate_BlocksWriteToolInPlanMode(t *testing.T) {
	m := newModule(t, &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalAuto})
	hook := gateOf(m)
	_, _, err := hook(context.Background(), &kernel.ToolCallInfo{
		Name: "edit_file", Effects: []kernel.ToolEffect{kernel.EffectWrite},
	})
	if err == nil {
		t.Fatal("write-effect tool must be blocked in plan mode")
	}
	if !IsDenial(err) {
		t.Fatalf("error is %v (%T), want a plan-mode Denial", err, err)
	}
	var d *Denial
	if !errors.As(err, &d) || d.Tool != "edit_file" {
		t.Fatalf("denial carries tool %q, want edit_file", d.Tool)
	}
}

func TestGate_BlocksExecAndUserDataEffects(t *testing.T) {
	m := newModule(t, &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalAuto})
	hook := gateOf(m)
	for _, effects := range [][]kernel.ToolEffect{
		{kernel.EffectExec},
		{kernel.EffectUserData},
		{kernel.EffectRead, kernel.EffectWrite},
	} {
		if _, _, err := hook(context.Background(), &kernel.ToolCallInfo{Name: "t", Effects: effects}); err == nil {
			t.Fatalf("effects %v must be blocked in plan mode", effects)
		}
	}
}

func TestGate_UndeclaredEffectsFailClosed(t *testing.T) {
	m := newModule(t, &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalAuto})
	hook := gateOf(m)
	if _, _, err := hook(context.Background(), &kernel.ToolCallInfo{Name: "undeclared"}); err == nil {
		t.Fatal("empty Effects must fail closed (conservative write+exec)")
	}
}

func TestGate_AllowsReadToolInPlanMode(t *testing.T) {
	m := newModule(t, &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalAuto})
	hook := gateOf(m)
	if _, _, err := hook(context.Background(), &kernel.ToolCallInfo{
		Name: "read_file", Effects: []kernel.ToolEffect{kernel.EffectRead},
	}); err != nil {
		t.Fatalf("read-effect tool must pass: %v", err)
	}
}

func TestGate_AllowsThePlanToolItself(t *testing.T) {
	m := newModule(t, &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalAuto})
	hook := gateOf(m)
	if _, _, err := hook(context.Background(), &kernel.ToolCallInfo{
		Name: DefaultToolName, Effects: []kernel.ToolEffect{kernel.EffectRead},
	}); err != nil {
		t.Fatalf("the plan tool is the sanctioned call and must pass: %v", err)
	}
}

func TestGate_NoopOutsidePlanMode(t *testing.T) {
	m := newModule(t, &gateFixture{mode: types.SessionModeNormal, decision: kernel.ApprovalAuto})
	hook := gateOf(m)
	if _, _, err := hook(context.Background(), &kernel.ToolCallInfo{
		Name: "edit_file", Effects: []kernel.ToolEffect{kernel.EffectWrite},
	}); err != nil {
		t.Fatalf("gate must be inert outside plan mode: %v", err)
	}
}

// ── exit_plan_mode ──────────────────────────────────────────────────────

func TestExit_ApprovedCallsOnApprove(t *testing.T) {
	f := &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalAllowedOnce}
	m := newModule(t, f)
	out, err := m.Tool().Run(context.Background(), `{"plan":"step one, step two","summary":"do it"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.approved) != 1 || f.approved[0] != "step one, step two" {
		t.Fatalf("OnApprove received %v", f.approved)
	}
	if out == "" {
		t.Fatal("approved exit must return an explanatory result")
	}
	if f.askCalls != 1 {
		t.Fatalf("Ask calls = %d, want 1", f.askCalls)
	}
}

func TestExit_DeniedKeepsPlanMode(t *testing.T) {
	f := &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalDeny}
	m := newModule(t, f)
	_, err := m.Tool().Run(context.Background(), `{"plan":"p"}`)
	if !IsDenial(err) {
		t.Fatalf("denied submission must return a Denial, got %v (%T)", err, err)
	}
	if len(f.approved) != 0 {
		t.Fatal("a denial must never call OnApprove")
	}
}

// TestExit_DeniedWithFeedbackCarriesTheHostReason proves the human's
// denial reason reaches the model verbatim inside the closed Denial.
func TestExit_DeniedWithFeedbackCarriesTheHostReason(t *testing.T) {
	f := &gateFixture{
		mode:     types.SessionModePlan,
		decision: kernel.ApprovalDeny,
		askErr:   &kernel.DeniedError{Feedback: "steps 3-4 conflict with the deployment window"},
	}
	m := newModule(t, f)
	_, err := m.Tool().Run(context.Background(), `{"plan":"p"}`)
	var d *Denial
	if !errors.As(err, &d) {
		t.Fatalf("denied-with-feedback must be a Denial, got %v (%T)", err, err)
	}
	if d.Feedback != "steps 3-4 conflict with the deployment window" {
		t.Fatalf("Feedback = %q, want the host's reason", d.Feedback)
	}
	if len(f.approved) != 0 {
		t.Fatal("a denial must never call OnApprove")
	}
}

func TestExit_ApprovalFailureIsOperationalNotDenial(t *testing.T) {
	f := &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalDeny, askErr: errors.New("host unreachable")}
	m := newModule(t, f)
	_, err := m.Tool().Run(context.Background(), `{"plan":"p"}`)
	if err == nil || IsDenial(err) {
		t.Fatalf("an approval failure is an operational error, got %v", err)
	}
	if len(f.approved) != 0 {
		t.Fatal("a failed approval must never call OnApprove")
	}
}

func TestExit_EmptyPlanRejected(t *testing.T) {
	f := &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalAuto}
	m := newModule(t, f)
	if _, err := m.Tool().Run(context.Background(), `{"plan":"   "}`); err == nil {
		t.Fatal("empty plan must be rejected")
	}
	if f.askCalls != 0 {
		t.Fatal("empty plan must not reach the approver")
	}
}

func TestExit_MissingRequiredPlanRejected(t *testing.T) {
	f := &gateFixture{mode: types.SessionModePlan, decision: kernel.ApprovalAuto}
	m := newModule(t, f)
	if _, err := m.Tool().Run(context.Background(), `{"summary":"no plan"}`); err == nil {
		t.Fatal("missing required plan argument must be rejected")
	}
}

func TestExit_OutsidePlanModeIsNoop(t *testing.T) {
	f := &gateFixture{mode: types.SessionModeNormal, decision: kernel.ApprovalAuto}
	m := newModule(t, f)
	if _, err := m.Tool().Run(context.Background(), `{"plan":"p"}`); err != nil {
		t.Fatalf("exit outside plan mode is a no-op success: %v", err)
	}
	if f.askCalls != 0 {
		t.Fatal("no approval traffic outside plan mode")
	}
}

// ── config ──────────────────────────────────────────────────────────────

func TestNew_RejectsIncompleteConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("nil Mode/Ask/OnApprove must be rejected")
	}
	if _, err := New(Config{Mode: func() types.SessionMode { return types.SessionModeNormal }}); err == nil {
		t.Fatal("nil Ask/OnApprove must be rejected")
	}
}

// TestMustNew_PanicsOnInvalidConfig: MustNew is the panic wrapper around
// New — an incomplete config panics instead of returning an error.
func TestMustNew_PanicsOnInvalidConfig(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustNew with an incomplete config must panic")
		}
	}()
	MustNew(Config{})
}

func TestTool_DeclaresNoMutatingEffects(t *testing.T) {
	f := &gateFixture{mode: types.SessionModeNormal, decision: kernel.ApprovalAuto}
	m := newModule(t, f)
	eff := kernel.EffectiveEffects(m.Tool())
	for _, e := range eff {
		if e == kernel.EffectWrite || e == kernel.EffectExec || e == kernel.EffectUserData {
			t.Fatalf("exit_plan_mode must not declare mutating effects, got %v", eff)
		}
	}
	if !hasEffect(eff, kernel.EffectRead) {
		t.Fatalf("exit_plan_mode should declare EffectRead, got %v", eff)
	}
}

func TestTool_NameFromConfig(t *testing.T) {
	m, err := New(Config{
		ToolName: "submit_plan",
		Mode:     func() types.SessionMode { return types.SessionModeNormal },
		Ask: func(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
			return kernel.ApprovalAuto, nil
		},
		OnApprove: func(string) {},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Tool().Name() != "submit_plan" {
		t.Fatalf("tool name = %q, want submit_plan", m.Tool().Name())
	}
	// The gate must exempt the configured name.
	r := &mockRegistrar{}
	m.Register(r)
	if _, _, err := r.toolCall(context.Background(), &kernel.ToolCallInfo{Name: "submit_plan", Effects: []kernel.ToolEffect{kernel.EffectRead}}); err != nil {
		t.Fatalf("configured tool name must pass the gate: %v", err)
	}
}

// ── log-as-state（DSH plan-mode 日志即状态）──────────────────────────────

// TestLogAsState_FoldsModeFromLog: with a log configured and no Mode
// callback, plan state folds from the session log — entry flips the fold,
// approval exits it, and a rebuilt log (restart/resume/fork) recovers the
// same state.
func TestLogAsState_FoldsModeFromLog(t *testing.T) {
	log := coresession.NewLog("s1")
	m, err := New(Config{
		Log: log,
		Ask: func(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
			return kernel.ApprovalAuto, nil
		},
		OnApprove: func(string) {},
	})
	if err != nil {
		t.Fatalf("New with log: %v", err)
	}
	if m.Mode() != types.SessionModeNormal {
		t.Fatal("fresh log folds to normal")
	}

	// Enter plan mode: the fold flips, and the event is durable.
	m.Enter()
	if m.Mode() != types.SessionModePlan {
		t.Fatal("after Enter the fold must be plan")
	}

	// The gate blocks writes under the log-folded mode.
	r := &mockRegistrar{}
	m.Register(r)
	if _, _, err := r.toolCall(context.Background(), &kernel.ToolCallInfo{Name: "write", Effects: []kernel.ToolEffect{kernel.EffectWrite}}); err == nil {
		t.Fatal("log-folded plan mode must block writes")
	}

	// Submit the plan: approval exits plan mode durably.
	if _, err := m.Tool().Run(context.Background(), `{"plan":"do it"}`); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if m.Mode() != types.SessionModeNormal {
		t.Fatal("after approval the fold must return to normal")
	}

	// Rebuild the log from its events (restart/resume): the state recovers.
	rebuilt := coresession.NewLog("s1")
	if err := rebuilt.Restore(log.Events()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	m2, err := New(Config{
		Log: rebuilt,
		Ask: func(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
			return kernel.ApprovalAuto, nil
		},
		OnApprove: func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Mode() != types.SessionModeNormal {
		t.Fatalf("rebuilt log must fold to normal after the approved exit, got %v", m2.Mode())
	}
}

// TestLogAsState_LiveModeWins: an explicit Mode callback takes precedence
// over the log fold.
func TestLogAsState_LiveModeWins(t *testing.T) {
	log := coresession.NewLog("s1")
	m, err := New(Config{
		Mode: func() types.SessionMode { return types.SessionModeNormal },
		Log:  log,
		Ask: func(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
			return kernel.ApprovalAuto, nil
		},
		OnApprove: func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Enter() // appends the event, but the live callback owns the answer
	if m.Mode() != types.SessionModeNormal {
		t.Fatal("live Mode callback must win over the log fold")
	}
}
