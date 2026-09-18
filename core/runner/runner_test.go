package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

type mockModel struct{}

func (m *mockModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return types.NewAssistantMessage("mock response"), &types.TokenUsage{TotalTokens: 10}, nil
}

func (m *mockModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}

func (m *mockModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return len(msgs) * 10, nil
}

// mockAgent implements kernel.Agent directly, replacing the old mockHandler + AgentAdapter pattern.
type mockAgent struct{}

func (a *mockAgent) Name() string        { return "mock-agent" }
func (a *mockAgent) Description() string { return "mock agent" }

func (a *mockAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	var msgs []*types.Message
	if input.SystemPrompt != "" {
		msgs = append(msgs, types.NewSystemMessage(input.SystemPrompt))
	}
	msgs = append(msgs, input.Messages...)

	select {
	case <-ctx.Done():
		return &kernel.Result{Err: ctx.Err()}
	default:
	}

	return &kernel.Result{
		Content:    "mock response",
		Messages:   msgs,
		TokenUsage: &types.TokenUsage{TotalTokens: 10},
	}
}

func TestNewRunner(t *testing.T) {
	agent := &mockAgent{}
	runner := NewRunner(agent, &mockModel{})
	if runner == nil {
		t.Fatal("runner is nil")
	}
}

func TestRunner_Run(t *testing.T) {
	agent := &mockAgent{}
	runner := NewRunner(agent, &mockModel{}, WithToolRegistry(tool.NewMapToolRegistry(nil)))
	info := runner.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	result, err := info.Result, info.Err
	if result.Err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TokenUsage == nil || result.TokenUsage.TotalTokens == 0 {
		t.Error("token usage not reported")
	}
}

// TestRunner_Run_DeliversTerminalEventsWithoutStreaming pins the event
// contract: event delivery is independent of generation mode. A plain Run
// with a StreamSender must still deliver the terminal event — the sender is
// the consumer's only channel, and EnableStreaming only switches the model
// call between Generate and Stream.
func TestRunner_Run_DeliversTerminalEventsWithoutStreaming(t *testing.T) {
	runner := NewRunner(&mockAgent{}, &mockModel{}, WithToolRegistry(tool.NewMapToolRegistry(nil)))
	var mu sync.Mutex
	var seen []types.EventType
	info := runner.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
		StreamSender: func(ev *types.Event) bool {
			mu.Lock()
			seen = append(seen, ev.Type)
			mu.Unlock()
			return true
		},
		// EnableStreaming deliberately false.
	})
	if info.Err != nil {
		t.Fatalf("Run = %v", info.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	foundFinish := false
	for _, et := range seen {
		if et == types.EventFinish {
			foundFinish = true
		}
	}
	if !foundFinish {
		t.Fatalf("events = %v, want EventFinish delivered without streaming", seen)
	}
}

// failingAgent returns an error result so the error event path is covered.
type failingAgent struct{}

func (a *failingAgent) Name() string        { return "failing-agent" }
func (a *failingAgent) Description() string { return "always fails" }
func (a *failingAgent) Run(_ context.Context, _ *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	return &kernel.Result{Err: errors.New("boom")}
}

func TestRunner_Run_DeliversErrorEventWithoutStreaming(t *testing.T) {
	runner := NewRunner(&failingAgent{}, &mockModel{}, WithToolRegistry(tool.NewMapToolRegistry(nil)))
	var mu sync.Mutex
	var seen []types.EventType
	info := runner.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
		StreamSender: func(ev *types.Event) bool {
			mu.Lock()
			seen = append(seen, ev.Type)
			mu.Unlock()
			return true
		},
	})
	if info.Err == nil {
		t.Fatal("Run = nil, want error")
	}
	mu.Lock()
	defer mu.Unlock()
	foundErr := false
	for _, et := range seen {
		if et == types.EventError {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatalf("events = %v, want EventError delivered without streaming", seen)
	}
}

func TestRunner_WithSystemPrompt(t *testing.T) {
	agent := &mockAgent{}
	runner := NewRunner(agent, &mockModel{}, WithToolRegistry(tool.NewMapToolRegistry(nil)))
	info := runner.Run(context.Background(), &types.AgentInput{
		SystemPrompt: "You are a helpful assistant.",
		Messages:     []*types.Message{types.NewUserMessage("hello")},
	})
	result, err := info.Result, info.Err
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = result
}

func TestRunner_WithTools(t *testing.T) {
	agent := &mockAgent{}
	reg := &mockRegistry{tools: nil}
	runner := NewRunner(agent, &mockModel{}, WithToolRegistry(reg))
	info := runner.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("test")},
	})
	result, err := info.Result, info.Err
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = result
}

type mockRegistry struct {
	tools []kernel.Tool
}

func (r *mockRegistry) List(ctx context.Context) []kernel.Tool {
	return r.tools
}

func (r *mockRegistry) Get(ctx context.Context, name string) kernel.Tool {
	for _, t := range r.tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func (r *mockRegistry) Add(ctx context.Context, tool kernel.Tool) error {
	r.tools = append(r.tools, tool)
	return nil
}

func (r *mockRegistry) Remove(ctx context.Context, name string) error {
	for i, t := range r.tools {
		if t.Name() == name {
			r.tools = append(r.tools[:i], r.tools[i+1:]...)
			return nil
		}
	}
	return nil
}

func (r *mockRegistry) AddSource(ctx context.Context, source kernel.ToolSource) error { return nil }
func (r *mockRegistry) RemoveSource(ctx context.Context, name string) error           { return nil }

type hookModule struct {
	t     *testing.T
	runFn func(t *testing.T, info *RunInfo)
}

func (m *hookModule) Register(rt kernel.HookRegistrar) {
	rt.OnAgentEnd(func(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
		m.runFn(m.t, info)
		return ctx, info, nil
	})
}

// providerAgent implements kernel.Agent and RunnerOptionProvider to verify
// that agent-level runner options survive any runner construction path.
type providerAgent struct {
	opts []RunnerOption
}

func (a *providerAgent) Name() string        { return "provider-agent" }
func (a *providerAgent) Description() string { return "agent with runner options" }
func (a *providerAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	return &kernel.Result{Content: "ok", Messages: input.Messages}
}
func (a *providerAgent) RunnerOptions() []RunnerOption { return a.opts }

func TestNewRunner_AppliesAgentProvidedOptions(t *testing.T) {
	observed := false
	agent := &providerAgent{opts: []RunnerOption{WithModule(&hookModule{
		t: t,
		runFn: func(t *testing.T, info *RunInfo) {
			observed = true
		},
	})}}

	// No explicit options: the agent-provided module must still be registered.
	r := NewRunner(agent, &mockModel{})
	r.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	if !observed {
		t.Fatal("agent-provided module hook was not fired")
	}
}

func TestNewRunner_MergesAgentAndExplicitModules(t *testing.T) {
	fromAgent, fromCaller := false, false
	agent := &providerAgent{opts: []RunnerOption{WithModule(&hookModule{
		t:     t,
		runFn: func(t *testing.T, info *RunInfo) { fromAgent = true },
	})}}

	r := NewRunner(agent, &mockModel{}, WithModule(&hookModule{
		t:     t,
		runFn: func(t *testing.T, info *RunInfo) { fromCaller = true },
	}))
	r.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	if !fromAgent {
		t.Fatal("agent-provided module was not registered")
	}
	if !fromCaller {
		t.Fatal("caller-explicit module was not registered")
	}
}

func TestNewRunner_ExplicitOptionsOverrideProvider(t *testing.T) {
	storeA := &mockCheckpointStore{cps: make(map[string]*types.Checkpoint)}
	storeB := &mockCheckpointStore{cps: make(map[string]*types.Checkpoint)}
	agent := &providerAgent{opts: []RunnerOption{WithRunnerCheckpointStore(storeA)}}

	r := NewRunner(agent, &mockModel{}, WithRunnerCheckpointStore(storeB))
	if r.checkpointStore != storeB {
		t.Fatal("caller-explicit option should override the agent-provided option")
	}
}

type mockCheckpointStore struct {
	cps map[string]*types.Checkpoint
}

func (m *mockCheckpointStore) Save(_ context.Context, cp *types.Checkpoint) error {
	m.cps[cp.ID] = cp
	return nil
}
func (m *mockCheckpointStore) Load(_ context.Context, id string) (*types.Checkpoint, error) {
	cp, ok := m.cps[id]
	if !ok {
		return nil, errors.New("checkpoint not found")
	}
	return cp, nil
}
func (m *mockCheckpointStore) Delete(_ context.Context, id string) error {
	delete(m.cps, id)
	return nil
}
func (m *mockCheckpointStore) List(_ context.Context) ([]string, error) {
	var ids []string
	for id := range m.cps {
		ids = append(ids, id)
	}
	return ids, nil
}

func TestRunner_HookAfterAgentRun(t *testing.T) {
	agent := &mockAgent{}
	var observed bool
	runner := NewRunner(agent, &mockModel{},
		WithToolRegistry(tool.NewMapToolRegistry(nil)),
		WithModule(&hookModule{
			t: t,
			runFn: func(t *testing.T, info *RunInfo) {
				observed = true
				if info.Result == nil || info.Result.Err != nil {
					t.Error("result not finished")
				}
			},
		}),
	)
	info := runner.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	result, err := info.Result, info.Err
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !observed {
		t.Error("HookAfterAgentRun was not called")
	}
	_ = result
}

func TestRunner_MultipleHookAfterAgentRun(t *testing.T) {
	agent := &mockAgent{}
	var obs1, obs2 bool
	runner := NewRunner(agent, &mockModel{},
		WithToolRegistry(tool.NewMapToolRegistry(nil)),
		WithModule(&hookModule{
			t:     t,
			runFn: func(t *testing.T, info *RunInfo) { obs1 = true },
		}),
		WithModule(&hookModule{
			t:     t,
			runFn: func(t *testing.T, info *RunInfo) { obs2 = true },
		}),
	)
	info := runner.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	if info.Err != nil {
		t.Fatalf("Run: %v", info.Err)
	}
	if !obs1 || !obs2 {
		t.Error("not all hook handlers were called")
	}
}

func TestRunner_ContextCancel(t *testing.T) {
	agent := &blockingAgent{}
	runner := NewRunner(agent, &mockModel{}, WithToolRegistry(tool.NewMapToolRegistry(nil)))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	info := runner.Run(ctx, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	if info.Err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

// blockingAgent blocks until context is cancelled.
type blockingAgent struct{}

func (a *blockingAgent) Name() string        { return "blocking" }
func (a *blockingAgent) Description() string { return "blocks until cancelled" }
func (a *blockingAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	<-ctx.Done()
	return &kernel.Result{Err: ctx.Err()}
}

type blockingModel struct{}

func (m *blockingModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func (m *blockingModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}

func (m *blockingModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return len(msgs) * 10, nil
}

func TestRunInfo_Fields(t *testing.T) {
	info := &RunInfo{
		AgentName: "test-agent",
		Result:    &kernel.Result{Content: "done"},
	}
	if info.AgentName != "test-agent" {
		t.Errorf("AgentName = %q", info.AgentName)
	}
	if info.Result.Err != nil {
		t.Error("Result should be finished")
	}
}

func TestRunner_HookAfterAgentRun_Error(t *testing.T) {
	agent := &blockingAgent{}
	var observedErr error
	runner := NewRunner(agent, &mockModel{},
		WithToolRegistry(tool.NewMapToolRegistry(nil)),
		WithModule(&hookModule{
			t: t,
			runFn: func(t *testing.T, info *RunInfo) {
				observedErr = info.Err
			},
		}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	runner.Run(ctx, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	if observedErr == nil {
		t.Error("HookAfterAgentRun should have received error with cancelled context")
	}
}

// ── Hook error propagation regression tests ─────────────────────────────

type rejectMessagesModule struct{}

func (m *rejectMessagesModule) Register(rt kernel.HookRegistrar) {
	rt.OnMessagesBuilt(func(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
		return ctx, msgs, errors.New("messages rejected by test")
	})
}

// messageBuildingAgent fires OnMessagesBuilt like an assembled agent does
// after building its messages (the hook moved from the Runner to the agent
// layer: the assembled agent fires it once after BuildMessages).
type messageBuildingAgent struct {
	mockAgent
}

func (a *messageBuildingAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	if _, _, err := rt.FireMessagesBuilt(ctx, input.Messages); err != nil {
		return &kernel.Result{Err: err}
	}
	return &kernel.Result{Content: "ok", Messages: input.Messages}
}

func TestRunner_HookMessagesBuilt_Rejects(t *testing.T) {
	agent := &messageBuildingAgent{}
	runner := NewRunner(agent, &mockModel{},
		WithToolRegistry(tool.NewMapToolRegistry(nil)),
		WithModule(&rejectMessagesModule{}),
	)

	info := runner.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("test")},
	})
	if info.Err == nil {
		t.Fatal("expected error from MessagesBuilt hook, got nil")
	}
	if !strings.Contains(info.Err.Error(), "messages rejected by test") {
		t.Fatalf("expected 'messages rejected by test' in error, got %q", info.Err.Error())
	}
	if info.Result == nil || info.Result.Err == nil {
		t.Errorf("expected agent Result to carry the hook error, got %+v", info.Result)
	}
}

type errorOnAfterRunModule struct{}

func (m *errorOnAfterRunModule) Register(rt kernel.HookRegistrar) {
	rt.OnAgentEnd(func(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
		return ctx, nil, errors.New("hook after agent run error")
	})
}

func TestRunner_HookAfterAgentRun_HookError(t *testing.T) {
	agent := &mockAgent{}
	runner := NewRunner(agent, &mockModel{},
		WithToolRegistry(tool.NewMapToolRegistry(nil)),
		WithModule(&errorOnAfterRunModule{}),
	)

	info := runner.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("test")},
	})
	if info.Err == nil {
		t.Fatal("expected error from HookAfterAgentRun, got nil")
	}
	if !strings.Contains(info.Err.Error(), "hook after agent run error") {
		t.Fatalf("expected 'hook after agent run error' in Err, got %q", info.Err.Error())
	}
	// Agent ran successfully — result should be present
	if info.Result == nil {
		t.Error("expected Result to be non-nil (agent ran successfully)")
	}
}

func TestRunner_HookAfterAgentRun_AgentErrorTakesPriority(t *testing.T) {
	agent := &mockAgent{}
	runner := NewRunner(agent, &mockModel{},
		WithToolRegistry(tool.NewMapToolRegistry(nil)),
		WithModule(&errorOnAfterRunModule{}),
	)

	// Cancel the context so the agent returns an error
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	info := runner.Run(ctx, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("test")},
	})
	if info.Err == nil {
		t.Fatal("expected error")
	}
	// The agent error (context canceled) should take priority, not the hook error
	if info.Err.Error() == "hook after agent run error" {
		t.Errorf("expected agent error to take priority over hook error, got hook error")
	}
}

// ── Runner wiring & robustness ───────────────────────────────────────────

// fakeAgent records the input/context it received so tests can observe
// runner wiring.
type fakeAgent struct {
	name      string
	lastInput *types.AgentInput
	lastState kernel.StateManager
}

func (a *fakeAgent) Name() string        { return a.name }
func (a *fakeAgent) Description() string { return "fake" }
func (a *fakeAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	a.lastInput = input
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		a.lastState = ac.State()
	}
	return &kernel.Result{Content: "done", Messages: input.Messages}
}

// TestRunner_AgentContextStateIsRuntimeState: the AgentContext must expose
// the Runtime's shared state — the contract's single source of truth (C2).
func TestRunner_AgentContextStateIsRuntimeState(t *testing.T) {
	sm := runtime.NewInMemoryState()
	fa := &fakeAgent{name: "a"}
	r := NewRunner(fa, nil, WithStateManager(sm))

	info := r.Run(context.Background(), &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}})
	if info.Err != nil {
		t.Fatalf("Run: %v", info.Err)
	}
	if fa.lastState == nil {
		t.Fatalf("agent saw no AgentContext state")
	}
	if fa.lastState != sm {
		t.Fatalf("AgentContext.State() is not the Runtime's state manager")
	}
}

// panicAgent panics inside Run.
type panicAgent struct{ name string }

func (a *panicAgent) Name() string        { return a.name }
func (a *panicAgent) Description() string { return "panics" }
func (a *panicAgent) Run(context.Context, *types.AgentInput, kernel.Runtime) *kernel.Result {
	panic("agent bug")
}

// TestRunner_AgentPanicBecomesResult: a panicking agent must surface as an
// error result (AgentEnd hooks still fire), never crash the process (C3).
func TestRunner_AgentPanicBecomesResult(t *testing.T) {
	r := NewRunner(&panicAgent{name: "p"}, nil)
	info := r.Run(context.Background(), &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}})
	if info.Err == nil {
		t.Fatal("panicking agent = nil error, want a panic error result")
	}
	if !strings.Contains(info.Err.Error(), "panicked") {
		t.Fatalf("error = %v, want panic marker", info.Err)
	}
	if info.Result == nil || info.Result.Reason != kernel.TerminateError {
		t.Fatalf("result = %+v, want TerminateError", info.Result)
	}
}
