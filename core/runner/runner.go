// Package runner provides the execution runner for gocel agents.
// It manages the full lifecycle: runtime construction, execution context
// creation, checkpoints, and run-level hooks.
package runner

import (
	"context"
	"fmt"
	"os"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

// ── RunInfo ─────────────────────────────────────────────────────────────

// RunInfo carries the result of a single Runner.Run call.
// Defined in kernel for hook access; re-exported here for convenience.
//
// RunInfo 携带单次 Runner.Run 调用的结果；定义于 kernel 供钩子访问，
// 此处仅为便利而再导出。
type RunInfo = kernel.RunInfo

// ── Runner ──────────────────────────────────────────────────────────────

// Runner orchestrates agent execution with Module hooks.
//
// 职责：构建 Runtime、装配模块、创建并注入 AgentContext（每次执行）、
// 触发 run 级钩子（AgentStart / AgentEnd）、支持 Stream 与 Resume。
//
// Runner orchestrates agent execution: it builds the Runtime, assembles
// modules, creates and injects the per-execution AgentContext, fires the
// run-level hooks, and supports streaming and resume.
type Runner struct {
	agent           kernel.Agent
	checkpointStore kernel.CheckpointStore
	krt             kernel.Runtime
	rtConcrete      *runtime.Runtime // concrete type for internal fire hooks
}

// runnerConfig collects RunnerOptions before constructing the Runner.
type runnerConfig struct {
	toolRegistry    kernel.ToolRegistry
	checkpointStore kernel.CheckpointStore
	modules         []kernel.Module
	runtimeOpts     []runtime.Option
}

// RunnerOption configures a Runner.
// RunnerOption 配置 Runner。
type RunnerOption func(*runnerConfig)

// RunnerOptionProvider is implemented by assembled agents that carry their
// own Runner configuration (tools, modules) built at construction time.
// NewRunner detects this interface and applies the provided options before
// the caller's explicit options, so agent-level configuration survives any
// runner construction path instead of being silently dropped.
//
// RunnerOptionProvider 由携带自身 Runner 配置（工具、模块）的装配产物 Agent 实现。
// NewRunner 会检测该接口，先应用其选项、再应用调用方显式选项，
// 保证 Agent 级配置在任何 Runner 构建路径下都不会静默丢失。
type RunnerOptionProvider interface {
	RunnerOptions() []RunnerOption
}

// WithToolRegistry sets a custom tool registry for the Runner.
// WithToolRegistry 为 Runner 设置自定义工具注册表。
func WithToolRegistry(tr kernel.ToolRegistry) RunnerOption {
	return func(c *runnerConfig) {
		c.toolRegistry = tr
	}
}

// WithRunnerCheckpointStore enables checkpointing for the Runner.
// WithRunnerCheckpointStore 为 Runner 启用检查点。
func WithRunnerCheckpointStore(store kernel.CheckpointStore) RunnerOption {
	return func(c *runnerConfig) {
		c.checkpointStore = store
	}
}

// WithModule registers a Module with the Runner.
// WithModule 向 Runner 注册一个 Module。
func WithModule(m kernel.Module) RunnerOption {
	return func(c *runnerConfig) {
		c.modules = append(c.modules, m)
	}
}

// WithRetryPolicy sets the retry policy for model calls.
// WithRetryPolicy 设置模型调用的重试策略。
func WithRetryPolicy(p runtime.RetryPolicy) RunnerOption {
	return func(c *runnerConfig) {
		c.runtimeOpts = append(c.runtimeOpts, runtime.WithRetryPolicy(p))
	}
}

// WithMiddleware adds middleware to the Runtime.
// 中间件位于 Runtime 内部、ChatModel 之上：只包裹模型调用。
func WithMiddleware(mws ...kernel.Middleware) RunnerOption {
	return func(c *runnerConfig) {
		c.runtimeOpts = append(c.runtimeOpts, runtime.WithMiddleware(mws...))
	}
}

// WithStateManager attaches a shared state manager to the Runtime.
// Use it to share state across runtimes (e.g. graph nodes).
//
// WithStateManager 向 Runtime 附加共享状态管理器，用于跨 Runtime 共享状态。
func WithStateManager(sm kernel.StateManager) RunnerOption {
	return func(c *runnerConfig) {
		c.runtimeOpts = append(c.runtimeOpts, runtime.WithStateManager(sm))
	}
}

// NewRunner creates a Runner for the given agent with the given model.
// If the agent implements RunnerOptionProvider, its options are applied
// first so that explicit caller options take precedence.
//
// NewRunner 为给定 agent 与 model 创建 Runner。若 agent 实现了
// RunnerOptionProvider，其选项会先被应用，显式调用方选项优先生效。
func NewRunner(agent kernel.Agent, model kernel.Model, opts ...RunnerOption) *Runner {
	cfg := &runnerConfig{}
	if p, ok := agent.(RunnerOptionProvider); ok {
		for _, opt := range p.RunnerOptions() {
			opt(cfg)
		}
	}
	for _, opt := range opts {
		opt(cfg)
	}
	r := &Runner{
		agent:           agent,
		checkpointStore: cfg.checkpointStore,
	}
	r.rtConcrete, r.krt = r.buildRuntime(model, cfg)
	return r
}

func (r *Runner) buildRuntime(model kernel.Model, cfg *runnerConfig) (*runtime.Runtime, kernel.Runtime) {
	rt := runtime.NewRuntime(model, cfg.toolRegistry, cfg.runtimeOpts...)
	for _, m := range cfg.modules {
		m.Register(rt)
	}
	return rt, rt
}

// ── AgentContext ─────────────────────────────────────────────────────────

// newAgentContext creates the per-execution AgentContext and wires the event
// sender and interrupt input from the input. Event delivery is independent
// of generation mode: a StreamSender is wired whenever provided, and
// EnableStreaming only decides whether the model call streams tokens.
func (r *Runner) newAgentContext(input *types.AgentInput, send func(*types.Event) bool) *runtime.AgentContext {
	opts := []runtime.AgentContextOption{runtime.WithContextAgentName(r.agent.Name())}
	// The AgentContext state is the Runtime's shared state — the contract's
	// single source of truth. Without this wiring, ac.State() and
	// rt.State() are two different stores and WithStateManager (shared
	// graph state) silently stops working (C2).
	if r.rtConcrete != nil && r.rtConcrete.State() != nil {
		opts = append(opts, runtime.WithContextState(r.rtConcrete.State()))
	}
	ac := runtime.NewAgentContext(opts...)
	// The framework fills what it knows (CWD) at run start; permission
	// tier, budget and counters are product-module responsibilities.
	if facts := ac.Facts(); facts != nil && facts.CWD == "" {
		facts.CWD, _ = os.Getwd()
	}
	if send != nil {
		ac.SetSendEvent(send)
	}
	if input != nil {
		if input.InterruptInput != nil {
			ac.SetInterruptInput(input.InterruptInput)
		}
		if input.StreamSender != nil {
			ac.SetSendEvent(input.StreamSender)
		}
	}
	return ac
}

// runAgentSafe runs the agent with panic recovery: a panicking agent must
// surface as an error result (AgentEnd hooks still fire), never crash the
// host process (C3).
func runAgentSafe(ctx context.Context, agent kernel.Agent, input *types.AgentInput, rt kernel.Runtime) (result *kernel.Result) {
	defer func() {
		if r := recover(); r != nil {
			result = &kernel.Result{
				Err:    fmt.Errorf("agent %q panicked: %v", agent.Name(), r),
				Reason: kernel.TerminateError,
			}
		}
	}()
	return agent.Run(ctx, input, rt)
}

// fireAgentStart fires the AgentStart hook and returns the updated context.
func (r *Runner) fireAgentStart(ctx context.Context, ac kernel.AgentContext, input *types.AgentInput) (context.Context, error) {
	if r.rtConcrete == nil {
		return ctx, nil
	}
	info := &kernel.AgentRunInfo{
		AgentName:    r.agent.Name(),
		InvocationID: ac.InvocationID(),
		RunPath:      ac.RunPath(),
		Branch:       ac.Branch(),
		Input:        input,
	}
	var err error
	ctx, _, err = r.rtConcrete.FireAgentStart(ctx, info)
	return ctx, err
}

// emitAfterRun fires the AgentEnd hook via the concrete runtime.
func (r *Runner) emitAfterRun(ctx context.Context, info *RunInfo) {
	if r.rtConcrete == nil {
		return
	}
	if _, _, err := r.rtConcrete.FireAgentEnd(ctx, info); err != nil && info.Err == nil {
		info.Err = err
	}
}

// Run executes the agent synchronously and returns a RunInfo. Terminal
// events (finish or error) are delivered through the input's StreamSender
// when one is provided — the same contract Stream provides, so consumers
// observe one uniform event stream regardless of generation mode.
//
// Run 同步执行 agent 并返回 RunInfo。提供 StreamSender 时，终态事件
// （finish 或 error）经其送达——与 Stream 相同的契约，消费方无论何种
// 生成模式都观察到统一的事件流。
func (r *Runner) Run(ctx context.Context, input *types.AgentInput) *RunInfo {
	ctx = kernel.WithRuntime(ctx, r.krt)

	ac := r.newAgentContext(input, nil)
	ctx = kernel.WithAgentContext(ctx, ac)

	if _, err := r.fireAgentStart(ctx, ac, input); err != nil {
		info := &RunInfo{InvocationID: ac.InvocationID(), AgentName: r.agent.Name(), Input: input, Err: err}
		r.emitAfterRun(ctx, info)
		return info
	}

	result := runAgentSafe(ctx, r.agent, input, r.krt)

	info := &RunInfo{
		InvocationID: ac.InvocationID(),
		AgentName:    r.agent.Name(),
		Input:        input,
		AllMsgs:      result.Messages,
		Result:       result,
		Err:          result.Err,
	}

	r.emitAfterRun(ctx, info)
	if send := ac.SendEvent(); send != nil {
		if result.Err != nil {
			send(&types.Event{Type: types.EventError, Content: result.Err.Error()})
		} else {
			send(&types.Event{
				Type:    types.EventFinish,
				Content: result.Content,
				Usage:   result.TokenUsage,
			})
		}
	}
	return info
}

// ── Stream ───────────────────────────────────────────────────────────────

// Stream executes the agent with streaming support, returning a StreamHandle.
// Stream 以流式方式执行 agent，返回一个 StreamHandle。
func (r *Runner) Stream(ctx context.Context, input *types.AgentInput) (*StreamHandle, error) {
	ctx = kernel.WithRuntime(ctx, r.krt)

	it, gen := kernel.NewAsyncIteratorPair[*types.Event]()

	// Never mutate the caller-owned input: wire the sender on a shallow
	// copy, so reusing or sharing the original input stays race-free and
	// consistent with Run's non-mutating behavior (C2).
	runInput := input
	if input != nil {
		cp := *input
		cp.StreamSender = gen.Send
		cp.EnableStreaming = true
		runInput = &cp
	}

	handle := &StreamHandle{
		AsyncIterator: it,
		done:          make(chan struct{}),
	}

	go func() {
		defer gen.Close()
		defer close(handle.done)

		ac := r.newAgentContext(runInput, gen.Send)
		runCtx := kernel.WithAgentContext(ctx, ac)

		if _, err := r.fireAgentStart(runCtx, ac, runInput); err != nil {
			gen.Send(&types.Event{Type: types.EventError, Content: err.Error()})
			return
		}

		result := runAgentSafe(runCtx, r.agent, runInput, r.krt)

		info := &RunInfo{
			InvocationID: ac.InvocationID(),
			AgentName:    r.agent.Name(),
			Input:        input,
			AllMsgs:      result.Messages,
			Result:       result,
			Err:          result.Err,
		}
		handle.result = info.Result

		r.emitAfterRun(runCtx, info)

		if result.Err != nil {
			gen.Send(&types.Event{Type: types.EventError, Content: result.Err.Error()})
			return
		}

		gen.Send(&types.Event{
			Type:    types.EventFinish,
			Content: result.Content,
			Usage:   result.TokenUsage,
		})
	}()

	return handle, nil
}

// ── Resume ───────────────────────────────────────────────────────────────

type resumeConfig struct {
	input  *types.AgentInput
	state  map[string]any
	branch string
}

// ResumeOption configures a Resume call.
// ResumeOption 配置一次 Resume 调用。
type ResumeOption func(*resumeConfig)

// WithModifiedInput replaces the AgentInput during resume.
// WithModifiedInput 在恢复期间替换 AgentInput。
func WithModifiedInput(input *types.AgentInput) ResumeOption {
	return func(rc *resumeConfig) { rc.input = input }
}

// WithModifiedState replaces the state map during resume.
// WithModifiedState 在恢复期间替换状态映射。
func WithModifiedState(state map[string]any) ResumeOption {
	return func(rc *resumeConfig) { rc.state = state }
}

// WithResumeBranch sets the branch path for conditional graph resume.
// WithResumeBranch 为条件图恢复设置分支路径。
func WithResumeBranch(branch string) ResumeOption {
	return func(rc *resumeConfig) { rc.branch = branch }
}

// Resume resumes execution from a saved checkpoint.
//
// 与"重放"不同：当 Agent 实现 kernel.Resumable 时，策略会话从检查点的
// 序列化状态恢复，Engine 从保存的步骤继续；消息历史同时被加载。
// 非装配型 Agent（无 Resumable）携带状态检查点恢复时显式报错（状态会被
// 丢弃）；无状态检查点退化为携带已保存消息的新执行。
//
// Unlike "replay": when the Agent implements kernel.Resumable, the policy
// session is restored from the checkpoint's serialized state and the engine
// continues from the saved step; the message history is loaded alongside.
// A stateful checkpoint on a non-Resumable agent fails loudly (the state
// would be discarded); a stateless checkpoint degrades to a fresh execution
// carrying the saved messages.
// messages.
func (r *Runner) Resume(ctx context.Context, checkpointID string, opts ...ResumeOption) (*RunInfo, error) {
	if r.checkpointStore == nil {
		return nil, fmt.Errorf("runner: no checkpoint store configured")
	}

	cp, err := r.checkpointStore.Load(ctx, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("runner: load checkpoint: %w", err)
	}
	// The checkpoint is a resume token, not a lock: it is consumed only
	// after the resumed run SUCCEEDS (see below). A failed restore or run
	// keeps it, so the resume stays retryable — deleting up front used to
	// burn the token on failure and swallow the delete error (C8).

	rc := &resumeConfig{}
	for _, opt := range opts {
		opt(rc)
	}

	interruptInput := make(chan string, 1)

	input := &types.AgentInput{
		Messages:        cp.Messages,
		SystemPrompt:    cp.SystemPrompt,
		EnableStreaming: cp.EnableStreaming,
		InterruptInput:  interruptInput,
	}
	// 剩余步数 = 预算 - 已走步数（"从断点继续"而非从头重放）。
	if cp.MaxSteps > 0 && cp.StepIndex > 0 {
		if remaining := cp.MaxSteps - cp.StepIndex; remaining > 0 {
			input.MaxSteps = remaining
		}
	}

	if rc.input != nil {
		if rc.input.Messages != nil {
			input.Messages = rc.input.Messages
		}
		if rc.input.SystemPrompt != "" {
			input.SystemPrompt = rc.input.SystemPrompt
		}
		if rc.input.EnableStreaming {
			input.EnableStreaming = true
		}
		if rc.input.MaxSteps > 0 {
			input.MaxSteps = rc.input.MaxSteps
		}
		if rc.input.InterruptInput != nil {
			input.InterruptInput = rc.input.InterruptInput
		}
		if rc.input.StreamSender != nil {
			// Event delivery is independent of generation mode, matching
			// Run: a resuming consumer receives the same event stream.
			input.StreamSender = rc.input.StreamSender
		}
	}

	ctx = kernel.WithRuntime(ctx, r.krt)

	// 先于会话恢复创建执行身份：ResumeSession 与错误上报都需要 InvocationID。
	ac := r.newAgentContext(input, nil)
	ctx = kernel.WithAgentContext(ctx, ac)

	// ── Resume 选项接线（C2）──
	// WithResumeBranch 写入 AgentContext 分支身份（运行路径观测/条件恢复）；
	// WithModifiedState 合并进共享状态（模块与工具经 ac.State()/rt.State()
	// 读到）。旧实现把两者写进无人读取的上下文键——文档化选项是空操作。
	if rc.branch != "" {
		ac.SetBranch(rc.branch)
	}
	if rc.state != nil && r.rtConcrete != nil && r.rtConcrete.State() != nil {
		for k, v := range rc.state {
			r.rtConcrete.State().Set(k, v)
		}
	}

	if _, err := r.fireAgentStart(ctx, ac, input); err != nil {
		info := &RunInfo{InvocationID: ac.InvocationID(), AgentName: r.agent.Name(), Input: input, Err: err, CheckpointID: checkpointID}
		r.emitAfterRun(ctx, info)
		return info, err
	}

	result := runAgentSafe(ctx, r.agent, input, r.krt)

	info := &RunInfo{
		InvocationID: ac.InvocationID(),
		AgentName:    r.agent.Name(),
		Input:        input,
		AllMsgs:      result.Messages,
		Result:       result,
		Err:          result.Err,
		CheckpointID: checkpointID,
	}

	r.emitAfterRun(ctx, info)

	// Terminal events mirror Run's contract (delivery independent of
	// generation mode).
	if send := ac.SendEvent(); send != nil {
		if result.Err != nil {
			send(&types.Event{Type: types.EventError, Content: result.Err.Error()})
		} else {
			send(&types.Event{
				Type:    types.EventFinish,
				Content: result.Content,
				Usage:   result.TokenUsage,
			})
		}
	}

	if result.Err != nil {
		return info, result.Err
	}

	// Success: consume the resume token. A delete failure is surfaced, not
	// swallowed — the run succeeded, but the checkpoint cleanup did not.
	if err := r.checkpointStore.Delete(ctx, checkpointID); err != nil {
		return info, fmt.Errorf("runner: delete consumed checkpoint: %w", err)
	}
	return info, nil
}
