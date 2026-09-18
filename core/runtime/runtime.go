package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
	"github.com/go-gocel/gocel/core/types"
)

// RetryPolicy configures retry behavior for model and tool calls.
// RetryPolicy 配置模型调用与工具调用的重试行为。
type RetryPolicy struct {
	MaxRetries   int
	BaseDelay    time.Duration
	MaxDelay     time.Duration
	RetryOnTool  bool
	RetryOnModel bool
}

// RuntimeConfig holds immutable configuration for a Runtime.
// RuntimeConfig 持有 Runtime 的不可变配置。
type RuntimeConfig struct {
	RetryPolicy RetryPolicy
}

// Runtime implements kernel.Runtime.
//
// 层级（自外向内）：模块钩子（run/循环级在 Runtime 之上由 Runner/引擎触发；
// 模型/工具级在 Runtime 内部触发）→ 重试 → 中间件链 → ChatModel。
// 中间件只能看到模型调用；钩子可以看到循环与工具。
type Runtime struct {
	model       kernel.ChatModel
	tools       kernel.ToolRegistry
	Config      RuntimeConfig
	middlewares []kernel.Middleware
	state       kernel.StateManager

	agentStartHooks  []kernel.AgentStartHook
	agentEndHooks    []kernel.AgentEndHook
	messagesHooks    []kernel.MessagesHook
	stepStartHooks   []kernel.StepHook
	stepEndHooks     []kernel.StepHook
	modelCallHooks   []kernel.ModelCallHook
	modelResultHooks []kernel.ModelResultHookFunc
	toolCallHooks    []kernel.ToolCallHook
	toolResultHooks  []kernel.ToolResultHookFunc
	decisionHooks    []kernel.DecisionHook
}

// ── Hook Registration (OnXxx) ───────────────────────────────────────────

// OnAgentStart registers an AgentStart hook and returns an unregister function.
// OnAgentStart 注册 AgentStart 钩子并返回注销函数。
func (rt *Runtime) OnAgentStart(fn kernel.AgentStartHook) func() {
	idx := len(rt.agentStartHooks)
	rt.agentStartHooks = append(rt.agentStartHooks, fn)
	return func() { rt.agentStartHooks[idx] = nil }
}

// OnAgentEnd registers an AgentEnd hook and returns an unregister function.
// OnAgentEnd 注册 AgentEnd 钩子并返回注销函数。
func (rt *Runtime) OnAgentEnd(fn kernel.AgentEndHook) func() {
	idx := len(rt.agentEndHooks)
	rt.agentEndHooks = append(rt.agentEndHooks, fn)
	return func() { rt.agentEndHooks[idx] = nil }
}

// OnMessagesBuilt registers a MessagesBuilt hook and returns an unregister function.
// OnMessagesBuilt 注册 MessagesBuilt 钩子并返回注销函数。
func (rt *Runtime) OnMessagesBuilt(fn kernel.MessagesHook) func() {
	idx := len(rt.messagesHooks)
	rt.messagesHooks = append(rt.messagesHooks, fn)
	return func() { rt.messagesHooks[idx] = nil }
}

// OnStepStart registers a StepStart hook and returns an unregister function.
// OnStepStart 注册 StepStart 钩子并返回注销函数。
func (rt *Runtime) OnStepStart(fn kernel.StepHook) func() {
	idx := len(rt.stepStartHooks)
	rt.stepStartHooks = append(rt.stepStartHooks, fn)
	return func() { rt.stepStartHooks[idx] = nil }
}

// OnStepEnd registers a StepEnd hook and returns an unregister function.
// OnStepEnd 注册 StepEnd 钩子并返回注销函数。
func (rt *Runtime) OnStepEnd(fn kernel.StepHook) func() {
	idx := len(rt.stepEndHooks)
	rt.stepEndHooks = append(rt.stepEndHooks, fn)
	return func() { rt.stepEndHooks[idx] = nil }
}

// OnModelCall registers a ModelCall hook and returns an unregister function.
// OnModelCall 注册 ModelCall 钩子并返回注销函数。
func (rt *Runtime) OnModelCall(fn kernel.ModelCallHook) func() {
	idx := len(rt.modelCallHooks)
	rt.modelCallHooks = append(rt.modelCallHooks, fn)
	return func() { rt.modelCallHooks[idx] = nil }
}

// OnModelResult registers a ModelResult hook and returns an unregister function.
// OnModelResult 注册 ModelResult 钩子并返回注销函数。
func (rt *Runtime) OnModelResult(fn kernel.ModelResultHookFunc) func() {
	idx := len(rt.modelResultHooks)
	rt.modelResultHooks = append(rt.modelResultHooks, fn)
	return func() { rt.modelResultHooks[idx] = nil }
}

// OnToolCall registers a ToolCall hook and returns an unregister function.
// OnToolCall 注册 ToolCall 钩子并返回注销函数。
func (rt *Runtime) OnToolCall(fn kernel.ToolCallHook) func() {
	idx := len(rt.toolCallHooks)
	rt.toolCallHooks = append(rt.toolCallHooks, fn)
	return func() { rt.toolCallHooks[idx] = nil }
}

// OnToolResult registers a ToolResult hook and returns an unregister function.
// OnToolResult 注册 ToolResult 钩子并返回注销函数。
func (rt *Runtime) OnToolResult(fn kernel.ToolResultHookFunc) func() {
	idx := len(rt.toolResultHooks)
	rt.toolResultHooks = append(rt.toolResultHooks, fn)
	return func() { rt.toolResultHooks[idx] = nil }
}

// OnDecision registers a Decision hook and returns an unregister function.
// OnDecision 注册 Decision 钩子并返回注销函数。
func (rt *Runtime) OnDecision(fn kernel.DecisionHook) func() {
	idx := len(rt.decisionHooks)
	rt.decisionHooks = append(rt.decisionHooks, fn)
	return func() { rt.decisionHooks[idx] = nil }
}

// ── Hook Firing (FireXxx) ───────────────────────────────────────────────

// fireHookSafe runs one hook with panic recovery: a panicking module hook
// must surface as an error (terminating the operation), never crash the
// host process (C3).
func fireHookSafe[T any](name string, fn func(context.Context, T) (context.Context, T, error), ctx context.Context, info T) (context.Context, T, error) {
	var outCtx context.Context
	var outInfo T
	var outErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				outErr = fmt.Errorf("hook %q panicked: %v", name, r)
			}
		}()
		outCtx, outInfo, outErr = fn(ctx, info)
	}()
	return outCtx, outInfo, outErr
}

// FireAgentStart fires all registered AgentStart hooks.
// FireAgentStart 触发所有已注册的 AgentStart 钩子。
func (rt *Runtime) FireAgentStart(ctx context.Context, info *kernel.AgentRunInfo) (context.Context, *kernel.AgentRunInfo, error) {
	if rt == nil {
		return ctx, info, nil
	}
	for _, fn := range rt.agentStartHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("agent_start", fn, ctx, info)
		if err != nil {
			return ctx, nil, err
		}
	}
	return ctx, info, nil
}

// FireAgentEnd fires all registered AgentEnd hooks.
// FireAgentEnd 触发所有已注册的 AgentEnd 钩子。
func (rt *Runtime) FireAgentEnd(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
	if rt == nil {
		return ctx, info, nil
	}
	for _, fn := range rt.agentEndHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("agent_end", fn, ctx, info)
		if err != nil {
			return ctx, nil, err
		}
	}
	return ctx, info, nil
}

// FireMessagesBuilt fires all registered MessagesBuilt hooks.
// FireMessagesBuilt 触发所有已注册的 MessagesBuilt 钩子。
func (rt *Runtime) FireMessagesBuilt(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
	if rt == nil {
		return ctx, msgs, nil
	}
	for _, fn := range rt.messagesHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, msgs, err = fireHookSafe("messages_built", fn, ctx, msgs)
		if err != nil {
			return ctx, nil, err
		}
	}
	// 数据处理：把钩子追加的多条 system 按列表顺序收敛为单条（前插/后插/中插由消息位置表达）。
	// 校验职责不在本处，由可选审计模块（module/msgcheck）在模型调用前兜底。
	return ctx, collapseSystem(msgs), nil
}

// collapseSystem 把消息列表归一化为「至多一条 system 且位于第 0 位」。
// 所有 system 消息按列表顺序用 "\n\n" 拼接为单条；非 system 消息原样保留。
// 前插 / 后插 / 中插由钩子通过消息在列表中的位置表达：
//   后插（默认，利于 prompt cache）: append(msgs, NewSystemMessage(section))
//   前插                              : append([]*Message{NewSystemMessage(section)}, msgs...)
//   中插                              : slices.Insert(msgs, i, NewSystemMessage(section))
func collapseSystem(msgs []*types.Message) []*types.Message {
	if len(msgs) == 0 {
		return msgs
	}
	var sections []string
	var others []*types.Message
	for _, m := range msgs {
		if m != nil && m.Role == types.RoleSystem {
			if m.Content != "" {
				sections = append(sections, m.Content)
			}
			continue
		}
		others = append(others, m)
	}
	if len(sections) == 0 {
		return msgs // 无 system 合法
	}
	out := make([]*types.Message, 0, len(others)+1)
	out = append(out, types.NewSystemMessage(strings.Join(sections, "\n\n")))
	return append(out, others...)
}

// FireStepStart fires all registered StepStart hooks. Returns
// (ctx, continue, info, error) — continue=false aborts the loop.
//
// The returned context carries the current step index (WithStepIndex), so
// model/tool hooks fired later within this step can correlate to it.
//
// FireStepStart 触发所有已注册的 StepStart 钩子，返回
// (ctx, continue, info, error)——continue=false 表示中止循环。
// 返回的 context 携带当前步索引（WithStepIndex），本步内稍后触发的
// 模型/工具钩子可据此关联。
func (rt *Runtime) FireStepStart(ctx context.Context, info *kernel.StepInfo) (context.Context, bool, *kernel.StepInfo, error) {
	if rt == nil {
		return ctx, true, info, nil
	}
	step := 0
	if info != nil {
		step = info.StepIndex
		if info.InvocationID == "" {
			info.InvocationID = invocationIDFromContext(ctx)
		}
	}
	ctx = kernel.WithStepIndex(ctx, step)
	if len(rt.stepStartHooks) == 0 {
		return ctx, true, info, nil
	}
	info.Continue = true
	for _, fn := range rt.stepStartHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("step_start", fn, ctx, info)
		if err != nil {
			return ctx, false, info, err
		}
		if info == nil {
			return ctx, true, nil, nil
		}
		if !info.Continue {
			return ctx, false, info, nil
		}
	}
	// Re-inject so the step survives hooks that replaced the context.
	return kernel.WithStepIndex(ctx, step), true, info, nil
}

// FireStepEnd fires all registered StepEnd hooks. Returns
// (ctx, continue, info, error) — continue=false aborts the loop.
//
// FireStepEnd 触发所有已注册的 StepEnd 钩子，返回
// (ctx, continue, info, error)——continue=false 表示中止循环。
func (rt *Runtime) FireStepEnd(ctx context.Context, info *kernel.StepInfo) (context.Context, bool, *kernel.StepInfo, error) {
	if rt == nil {
		return ctx, true, info, nil
	}
	if info != nil && info.InvocationID == "" {
		info.InvocationID = invocationIDFromContext(ctx)
	}
	if len(rt.stepEndHooks) == 0 {
		return ctx, true, info, nil
	}
	info.Continue = true
	for _, fn := range rt.stepEndHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("step_end", fn, ctx, info)
		if err != nil {
			return ctx, false, info, err
		}
		if info == nil {
			return ctx, true, nil, nil
		}
		if !info.Continue {
			return ctx, false, info, nil
		}
	}
	return ctx, true, info, nil
}

// RunStep runs one step of the loop with its hook lifecycle:
// FireStepStart → body → FireStepEnd. The engine writes only the
// orchestration (body); hook firing and continue/err handling are
// centralized here.
//
// RunStep 执行「一步」的钩子生命周期：StepStart → body → StepEnd。
// 引擎只写编排（body）；钩子触发与 continue/err 处理统一收紧在此。
//
// 语义：
//  1. 先 FireStepStart；若 cont=false 或 err!=nil，直接返回，
//     不执行 body、不触发 StepEnd（一步从未开始）。
//  2. 执行 body（引擎编排）。body 通过修改 info 回写
//     Messages / HasToolCalls。
//  3. defer 保证 StepEnd 覆盖 body 的每一条退出路径
//     （正常返回 / 返回 error / panic）。StepEnd 读取 info 的最终字段，
//     其 cont/err 合并进返回值。
//
// 返回 (outCtx, cont, outInfo, err)：cont 是 StepStart 与 StepEnd 的
// continue 合并结果；err 是 body 错误与 StepEnd 钩子错误的合并结果
// （body 错误为主错误）。
func (rt *Runtime) RunStep(
	ctx context.Context,
	info *kernel.StepInfo,
	body func(context.Context, *kernel.StepInfo) (context.Context, *kernel.StepInfo, error),
) (outCtx context.Context, cont bool, outInfo *kernel.StepInfo, err error) {
	if rt == nil {
		return ctx, true, info, nil
	}
	outCtx, cont, outInfo, err = rt.FireStepStart(ctx, info)
	if err != nil || !cont {
		return outCtx, cont, outInfo, err
	}
	// StepEnd 必须覆盖 body 的所有退出路径（正常 / error / panic）。
	defer func() {
		endCtx, endCont, endInfo, endErr := rt.FireStepEnd(outCtx, outInfo)
		outCtx = endCtx
		cont = endCont
		if endInfo != nil {
			outInfo = endInfo
		}
		if err == nil {
			err = endErr
		} else if endErr != nil {
			err = errors.Join(err, endErr)
		}
	}()
	outCtx, outInfo, err = body(outCtx, outInfo)
	return outCtx, cont, outInfo, err
}

// fireModelCall fires all registered ModelCall hooks (before a model call).
func (rt *Runtime) fireModelCall(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
	if rt == nil || len(rt.modelCallHooks) == 0 {
		return ctx, msgs, nil
	}
	info := &kernel.ModelCallInfo{Messages: msgs}
	rt.enrichModelInfo(ctx, info)
	for _, fn := range rt.modelCallHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("model_call", fn, ctx, info)
		if err != nil {
			return ctx, nil, err
		}
		if info != nil && info.Messages != nil {
			msgs = info.Messages
		}
	}
	return ctx, msgs, nil
}

// fireModelResult fires all registered ModelResult hooks (after a model call).
func (rt *Runtime) fireModelResult(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	if rt == nil {
		return ctx, info, nil
	}
	rt.enrichModelInfo(ctx, info)
	for _, fn := range rt.modelResultHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("model_result", fn, ctx, info)
		if err != nil {
			return ctx, nil, err
		}
	}
	return ctx, info, nil
}

// fireToolCall fires all registered ToolCall hooks (before a tool runs).
func (rt *Runtime) fireToolCall(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	if rt == nil {
		return ctx, info, nil
	}
	rt.enrichToolInfo(ctx, info)
	for _, fn := range rt.toolCallHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("tool_call", fn, ctx, info)
		if err != nil {
			return ctx, nil, err
		}
	}
	return ctx, info, nil
}

// fireToolResult fires all registered ToolResult hooks (after a tool ran).
func (rt *Runtime) fireToolResult(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	if rt == nil {
		return ctx, info, nil
	}
	rt.enrichToolInfo(ctx, info)
	for _, fn := range rt.toolResultHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("tool_result", fn, ctx, info)
		if err != nil {
			return ctx, nil, err
		}
	}
	return ctx, info, nil
}

// ── Correlation enrichment ──────────────────────────────────────────────
//
// 运行/步骤关联信息（InvocationID/AgentName/StepIndex）在钩子触发前统一
// 填充：单一来源是 AgentContext（运行身份）与 FireStepStart 注入的步骤游标。

// correlation reads run/step identity from ctx. StepIndex is -1 when the
// call happens outside a step loop.
func correlation(ctx context.Context) (invocationID, agentName string, stepIndex int) {
	stepIndex = -1
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		invocationID = ac.InvocationID()
		agentName = ac.AgentName()
	}
	if n, ok := kernel.StepIndexFromContext(ctx); ok {
		stepIndex = n
	}
	return invocationID, agentName, stepIndex
}

// invocationIDFromContext extracts just the invocation id (default "").
func invocationIDFromContext(ctx context.Context) string {
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		return ac.InvocationID()
	}
	return ""
}

func (rt *Runtime) enrichModelInfo(ctx context.Context, info *kernel.ModelCallInfo) {
	if info == nil {
		return
	}
	info.InvocationID, info.AgentName, info.StepIndex = correlation(ctx)
}

func (rt *Runtime) enrichToolInfo(ctx context.Context, info *kernel.ToolCallInfo) {
	if info == nil {
		return
	}
	info.InvocationID, info.AgentName, info.StepIndex = correlation(ctx)
	// Effects are the single source for guard/approval modules: resolved
	// once here from the tool's declaration (conservative default when
	// undeclared), never re-derived by each consumer.
	if info.Name != "" {
		for _, t := range rt.tools.List(ctx) {
			if t.Name() == info.Name {
				info.Effects = kernel.EffectiveEffects(t)
				break
			}
		}
	}
}

// FireDecision fires all registered Decision hooks after enriching the info
// with run/step identity from the context.
//
// FireDecision 先用上下文中的运行/步身份信息充实 info，再触发所有已注册的
// Decision 钩子。
func (rt *Runtime) FireDecision(ctx context.Context, info *kernel.DecisionInfo) error {
	if rt == nil {
		return nil
	}
	if info != nil {
		info.InvocationID, info.AgentName, info.StepIndex = correlation(ctx)
	}
	for _, fn := range rt.decisionHooks {
		if fn == nil {
			continue
		}
		var err error
		ctx, info, err = fireHookSafe("decision", fn, ctx, info)
		if err != nil {
			return err
		}
	}
	return nil
}

// ── Construction ─────────────────────────────────────────────────────────

// NewRuntime creates a new Runtime.
// NewRuntime 创建一个新的 Runtime。
func NewRuntime(m kernel.Model, tools kernel.ToolRegistry, opts ...Option) *Runtime {
	var cm kernel.ChatModel
	if m != nil {
		var ok bool
		cm, ok = m.(kernel.ChatModel)
		if !ok {
			cm = &modelAdapter{model: m}
		}
	}
	rt := &Runtime{
		model: cm,
		tools: tools,
		state: NewInMemoryState(),
	}
	for _, opt := range opts {
		opt(rt)
	}
	if len(rt.middlewares) > 0 && rt.model != nil {
		rt.model = &middlewareModel{
			inner: rt.model,
			mws:   rt.middlewares,
		}
	}
	return rt
}

// Option configures a Runtime during construction.
// Option 在构造期间配置 Runtime。
type Option func(*Runtime)

// WithRetryPolicy sets the retry policy.
// WithRetryPolicy 设置重试策略。
func WithRetryPolicy(p RetryPolicy) Option {
	return func(rt *Runtime) {
		if p.MaxRetries > 0 || p.BaseDelay > 0 || p.MaxDelay > 0 || p.RetryOnTool || p.RetryOnModel {
			rt.Config.RetryPolicy = p
		}
	}
}

// WithMiddleware adds middleware to the Runtime. Middleware wraps model calls
// (Generate/Stream) — the innermost layer above ChatModel.
//
// WithMiddleware 向 Runtime 添加中间件。中间件包裹模型调用
// （Generate/Stream）——ChatModel 之上的最内层。
func WithMiddleware(mws ...kernel.Middleware) Option {
	return func(rt *Runtime) {
		rt.middlewares = append(rt.middlewares, mws...)
	}
}

// WithStateManager attaches a shared StateManager instead of the default
// in-memory one. Use it to share state across runtimes (e.g. graph nodes).
//
// WithStateManager 附加共享 StateManager 以替代默认内存实现。
// 用于跨 Runtime 共享状态（如图节点）。
func WithStateManager(sm kernel.StateManager) Option {
	return func(rt *Runtime) {
		if sm != nil {
			rt.state = sm
		}
	}
}

// State returns the shared state manager of this runtime.
// State 返回本 runtime 的共享状态管理器。
func (rt *Runtime) State() kernel.StateManager {
	if rt == nil || rt.state == nil {
		return nil
	}
	return rt.state
}

// ── Safe accessors ──────────────────────────────────────────────────────

// CountTokens counts the tokens of the given messages using the model.
// CountTokens 使用模型统计给定消息的 token 数量。
func (rt *Runtime) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	if rt == nil || rt.model == nil {
		return 0, kernel.ErrNilModel
	}
	return rt.model.CountTokens(ctx, msgs, opts...)
}

// ListTools returns the tools registered in this runtime.
// ListTools 返回本 runtime 中已注册的工具。
func (rt *Runtime) ListTools(ctx context.Context) []kernel.Tool {
	if rt == nil || rt.tools == nil {
		return nil
	}
	return rt.tools.List(ctx)
}

// Register registers the tools provided by the given provider.
// Register 注册给定 provider 提供的工具。
func (rt *Runtime) Register(ctx context.Context, provider kernel.ToolProvider) error {
	if rt == nil || rt.tools == nil {
		return kernel.ErrNilTools
	}
	tools, err := provider.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("register %q: %w", provider.Name(), err)
	}
	// A provider exposing zero tools registers nothing (legal: an MCP server
	// may expose no tools). This must never panic — and must never pretend
	// tools were added.
	if len(tools) == 0 {
		return nil
	}
	// Single-tool provider whose Name() matches the tool → register as-is.
	// Multi-tool provider → prefix each tool with "providerName/toolName".
	addPrefix := len(tools) > 1 || provider.Name() != tools[0].Name()
	for _, t := range tools {
		name := t.Name()
		if addPrefix {
			name = provider.Name() + "/" + name
		}
		tw := &prefixedTool{inner: t, name: name}
		if err := rt.tools.Add(ctx, tw); err != nil {
			return err
		}
	}
	return nil
}

// Unregister removes the tool with the given name.
// Unregister 移除指定名称的工具。
func (rt *Runtime) Unregister(ctx context.Context, name string) error {
	if rt == nil || rt.tools == nil {
		return kernel.ErrNilTools
	}
	return rt.tools.Remove(ctx, name)
}

// prefixedTool wraps a kernel.Tool with a modified name for namespace disambiguation.
type prefixedTool struct {
	inner kernel.Tool
	name  string
}

// Name returns the prefixed tool name.
// Name 返回带前缀的工具名称。
func (p *prefixedTool) Name() string           { return p.name }
// Description returns the inner tool's description.
// Description 返回内部工具的描述。
func (p *prefixedTool) Description() string    { return p.inner.Description() }
// Schema returns the inner tool's schema.
// Schema 返回内部工具的 schema。
func (p *prefixedTool) Schema() map[string]any { return p.inner.Schema() }
// Run executes the inner tool with the given arguments.
// Run 用给定参数执行内部工具。
func (p *prefixedTool) Run(ctx context.Context, args string) (string, error) {
	return p.inner.Run(ctx, args)
}
// ToolMeta returns the inner tool's metadata.
// ToolMeta 返回内部工具的元数据。
func (p *prefixedTool) ToolMeta() kernel.ToolMeta { return p.inner.ToolMeta() }

// ── Execution methods ───────────────────────────────────────────────────

// CallModel calls the model with the given messages and generation options.
// CallModel 用给定消息与生成选项调用模型。
func (rt *Runtime) CallModel(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	if rt == nil || rt.model == nil {
		return nil, nil, kernel.ErrNilModel
	}

	var err error
	ctx, msgs, err = rt.fireModelCall(ctx, msgs)
	if err != nil {
		return nil, nil, err
	}

	var callCfg kernel.GenConfig
	callCfg.Apply(opts)
	if callCfg.Tools == nil && rt.tools != nil {
		tools := rt.tools.List(ctx)
		if len(tools) > 0 {
			opts = append([]kernel.GenOption{kernel.WithTools(kernel.ToolFromTools(tools))}, opts...)
		}
	}

	policy := rt.Config.RetryPolicy
	// A negative budget (caller error) must not skip the model call
	// entirely or produce a bogus "failed after -1 retries" error.
	if policy.MaxRetries < 0 {
		policy.MaxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		if attempt > 0 {
			if !policy.RetryOnModel {
				break
			}
			if !kernel.IsRetryableError(lastErr) {
				break
			}
			if err := waitRetryDelay(ctx, policy, attempt); err != nil {
				return nil, nil, ctx.Err()
			}
		}

		resp, usage, err := rt.model.Generate(ctx, msgs, opts...)

		if err == nil {
			info := &kernel.ModelCallInfo{Messages: msgs, Response: resp, Usage: usage}
			var mcInfo *kernel.ModelCallInfo
			_, mcInfo, hookErr := rt.fireModelResult(ctx, info)
			if hookErr != nil {
				return nil, nil, hookErr
			}
			if mcInfo != nil {
				resp = mcInfo.Response
				usage = mcInfo.Usage
			}
			return resp, usage, nil
		}

		info := &kernel.ModelCallInfo{Messages: msgs, Error: err}
		_, _, hookErr := rt.fireModelResult(ctx, info)
		if hookErr != nil {
			return nil, nil, fmt.Errorf("hook after model call: %w (original: %w)", hookErr, err)
		}
		lastErr = err
	}

	return nil, nil, fmt.Errorf("model call failed after %d retries: %w", policy.MaxRetries, lastErr)
}

// CallModelStream starts a streaming model call, returning a StreamReader.
// CallModelStream 启动流式模型调用，返回一个 StreamReader。
func (rt *Runtime) CallModelStream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	if rt == nil || rt.model == nil {
		return nil, kernel.ErrNilModel
	}

	var err error
	ctx, msgs, err = rt.fireModelCall(ctx, msgs)
	if err != nil {
		return nil, err
	}

	var streamCfg kernel.GenConfig
	streamCfg.Apply(opts)
	if streamCfg.Tools == nil && rt.tools != nil {
		tools := rt.tools.List(ctx)
		if len(tools) > 0 {
			opts = append([]kernel.GenOption{kernel.WithTools(kernel.ToolFromTools(tools))}, opts...)
		}
	}

	raw, err := rt.model.Stream(ctx, msgs, opts...)
	if err != nil {
		return nil, err
	}

	return &hookStreamReader{
		ctx:  ctx,
		rt:   rt,
		msgs: msgs,
		raw:  raw,
		opts: opts,
	}, nil
}

// hookStreamReader wraps a raw StreamReader to fire ModelResult hooks on completion.
type hookStreamReader struct {
	ctx    context.Context
	rt     *Runtime
	msgs   []*types.Message
	raw    kernel.StreamReader
	opts   []kernel.GenOption
	closed atomic.Bool
}

// Recv receives the next stream chunk, firing ModelResult hooks at EOF.
// Recv 接收下一个流分片，在 EOF 时触发 ModelResult 钩子。
func (r *hookStreamReader) Recv() (*types.Message, error) {
	chunk, err := r.raw.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) && !r.closed.Swap(true) {
			resp := &types.Message{Role: types.RoleAssistant}
			if chunk != nil {
				resp.Content = chunk.Content
				resp.ReasoningContent = chunk.ReasoningContent
				resp.ToolCalls = chunk.ToolCalls
			}
			if r.rt != nil {
				info := &kernel.ModelCallInfo{Messages: r.msgs, Response: resp}
				if _, _, hookErr := r.rt.fireModelResult(r.ctx, info); hookErr != nil {
					return chunk, hookErr
				}
			}
			// Release the underlying stream at EOF: Close() after EOF is a
			// no-op, so without this the HTTP body/connection never closes.
			_ = r.raw.Close()
		}
		return chunk, err
	}
	return chunk, nil
}

// Close closes the underlying stream; repeated calls are no-ops.
// Close 关闭底层流；重复调用为空操作。
func (r *hookStreamReader) Close() error {
	if !r.closed.Swap(true) {
		return r.raw.Close()
	}
	return nil
}

// Done returns the channel that signals stream completion.
// Done 返回标识流结束的通道。
func (r *hookStreamReader) Done() <-chan struct{} { return r.raw.Done() }

// ── modelAdapter ────────────────────────────────────────────────────────────

type modelAdapter struct {
	model kernel.Model
}

// Generate always fails because modelAdapter only adapts non-ChatModel models.
// Generate 始终失败：modelAdapter 只适配非 ChatModel 模型。
func (a *modelAdapter) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return nil, nil, fmt.Errorf("model %T does not implement kernel.ChatModel; cannot generate", a.model)
}

// Stream always fails because modelAdapter only adapts non-ChatModel models.
// Stream 始终失败：modelAdapter 只适配非 ChatModel 模型。
func (a *modelAdapter) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, fmt.Errorf("model %T does not implement kernel.ChatModel; cannot stream", a.model)
}

// CountTokens delegates token counting to the wrapped model.
// CountTokens 将 token 统计委托给被包装的模型。
func (a *modelAdapter) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return a.model.CountTokens(ctx, messages, opts...)
}

// ── middlewareModel ─────────────────────────────────────────────────────────
//
// 层级位置：Runtime 内部、ChatModel 之上。中间件只包裹模型调用，
// 不可见循环、工具与状态（那是模块钩子层的职责）。

type middlewareModel struct {
	inner kernel.ChatModel
	mws   []kernel.Middleware
}

// Generate runs the model call through the middleware chain.
// Generate 经中间件链执行模型调用。
func (m *middlewareModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	handler := func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return m.inner.Generate(ctx, msgs, opts...)
	}
	return kernel.Apply(handler, m.mws...)(ctx, msgs)
}

// Stream runs the streaming model call through the middleware chain.
// Stream 经中间件链执行流式模型调用。
func (m *middlewareModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	handler := func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		return m.inner.Stream(ctx, msgs, opts...)
	}
	return kernel.ApplyStream(handler, m.mws...)(ctx, msgs)
}

// CountTokens delegates token counting to the wrapped model.
// CountTokens 将 token 统计委托给被包装的模型。
func (m *middlewareModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return m.inner.CountTokens(ctx, msgs, opts...)
}

// ExecTools executes a batch of tool calls with hook firing.
//
// Tool execution failures are retried per RetryPolicy.RetryOnTool: failed
// calls are re-executed with exponential backoff up to MaxRetries. Calls
// blocked by ToolCall hooks are never retried (they return early).
//
// ExecTools 执行一批工具调用并触发钩子。
// 工具执行失败按 RetryPolicy.RetryOnTool 重试：失败调用以指数退避重新执行，
// 最多 MaxRetries 次。被 ToolCall 钩子拦截的调用不重试（提前返回）。
//
// retryDelay computes the exponential backoff delay for the given attempt
// (attempt starts at 1). Falls back to 1s base and caps at MaxDelay.
func retryDelay(policy RetryPolicy, attempt int) time.Duration {
	delay := policy.BaseDelay
	if delay == 0 {
		delay = time.Second
	}
	delay *= 1 << uint(attempt-1)
	if policy.MaxDelay > 0 && delay > policy.MaxDelay {
		delay = policy.MaxDelay
	}
	return delay
}

// waitRetryDelay sleeps for the backoff delay, aborting on context cancel.
func waitRetryDelay(ctx context.Context, policy RetryPolicy, attempt int) error {
	delay := retryDelay(policy, attempt)
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// execToolsWithRetry executes tool calls concurrently, retrying failed
// calls while RetryPolicy.RetryOnTool is enabled. Guard-blocked calls never
// reach this path (ExecTools returns early on blocked), so retries only
// apply to genuine execution failures. A call that keeps failing ends with
// its error embedded in the message, exactly like a single-shot run.
func (rt *Runtime) execToolsWithRetry(ctx context.Context, toolCalls []*types.ToolCall) []*types.Message {
	// Per-tool declared budget (DSH tool-call-timeout separation): the tool
	// declares TimeoutMs in its ToolMeta, the mechanism enforces it here as
	// a cooperative deadline per call while calls still run concurrently.
	timeoutOf := func(toolName string) time.Duration {
		if tl := rt.tools.Get(ctx, toolName); tl != nil {
			if ms := tl.ToolMeta().TimeoutMs; ms > 0 {
				return time.Duration(ms) * time.Millisecond
			}
		}
		return 0
	}
	exec := func(tcs []*types.ToolCall) []orchestrate.ToolExecResult {
		return orchestrate.ExecToolsConcurrentResults(ctx, tcs, rt.tools,
			orchestrate.WithCallTimeout(timeoutOf))
	}
	results := exec(toolCalls)

	policy := rt.Config.RetryPolicy
	if !policy.RetryOnTool || policy.MaxRetries <= 0 {
		return toolResultMessages(results)
	}

	for attempt := 1; attempt <= policy.MaxRetries; attempt++ {
		var retryIdx []int
		var retryCalls []*types.ToolCall
		for i, r := range results {
			if r.Err != nil {
				retryIdx = append(retryIdx, i)
				retryCalls = append(retryCalls, toolCalls[i])
			}
		}
		if len(retryIdx) == 0 {
			break
		}
		if err := waitRetryDelay(ctx, policy, attempt); err != nil {
			break // context cancelled — keep the last results
		}
		retryResults := exec(retryCalls)
		for j, idx := range retryIdx {
			results[idx] = retryResults[j]
		}
	}

	return toolResultMessages(results)
}

// jsonErrorContent builds a JSON-safe {"error": ...} tool-message payload:
// raw error text (quotes, newlines) must never break the message JSON that
// is fed back to the LLM provider.
func jsonErrorContent(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

// toolResultMessages flattens ToolExecResult into the message list.
func toolResultMessages(results []orchestrate.ToolExecResult) []*types.Message {
	msgs := make([]*types.Message, len(results))
	for i, r := range results {
		msgs[i] = r.Message
	}
	return msgs
}

// ExecTools executes a batch of tool calls with hook firing and returns the
// resulting tool messages.
//
// ExecTools 执行一批工具调用并触发钩子，返回对应的工具消息。
func (rt *Runtime) ExecTools(ctx context.Context, toolCalls []*types.ToolCall) []*types.Message {
	if rt == nil || len(toolCalls) == 0 {
		return nil
	}
	if rt.tools == nil {
		results := make([]*types.Message, len(toolCalls))
		for i := range toolCalls {
			kernel.NormalizeToolCall(toolCalls[i])
			results[i] = types.NewToolMessage(
				`{"error":"no tool registry configured"}`,
				toolCalls[i].ID, toolCalls[i].Function.Name,
			)
		}
		return results
	}

	results := make([]*types.Message, len(toolCalls))
	var blocked bool
	for i := range toolCalls {
		kernel.NormalizeToolCall(toolCalls[i])
		tcInfo := &kernel.ToolCallInfo{
			Name: toolCalls[i].Function.Name,
			Args: toolCalls[i].Function.Arguments,
		}
		var tci *kernel.ToolCallInfo
		_, tci, err := rt.fireToolCall(ctx, tcInfo)
		if err != nil {
			results[i] = types.NewToolMessage(
				jsonErrorContent("tool call blocked: "+err.Error()),
				toolCalls[i].ID, toolCalls[i].Function.Name,
			)
			blocked = true
		}
		if tci != nil {
			toolCalls[i].Function.Name = tci.Name
			toolCalls[i].Function.Arguments = tci.Args
		}
	}
	if blocked {
		// 批次因任一调用被钩子拦截而整体中止：未被拦截的调用不会执行，
		// 但每条 tool_call 仍必须产出对应的 tool 消息——LLM 提供商要求
		// assistant 消息的 tool_calls 被逐条 tool 消息接续，缺一条即拒绝
		// （openai 400: insufficient tool messages following tool_calls）。
		for i := range results {
			if results[i] == nil {
				results[i] = types.NewToolMessage(
					`{"error":"tool call skipped: batch aborted by tool call hook"}`,
					toolCalls[i].ID, toolCalls[i].Function.Name,
				)
			}
		}
		return results
	}

	results = rt.execToolsWithRetry(ctx, toolCalls)

	for i, msg := range results {
		if msg == nil || i >= len(toolCalls) {
			continue
		}
		info := &kernel.ToolCallInfo{
			Name:   msg.ToolName,
			Args:   toolCalls[i].Function.Arguments,
			Result: msg.Content,
			Parts:  msg.ContentParts,
		}
		var tci *kernel.ToolCallInfo
		_, tci, err := rt.fireToolResult(ctx, info)
		if err != nil {
			results[i] = types.NewToolMessage(
				jsonErrorContent("tool result rejected: "+err.Error()),
				toolCalls[i].ID, toolCalls[i].Function.Name,
			)
		} else if tci != nil {
			results[i].Content = tci.Result
			// nil Parts means "hook did not touch parts" — keep the tool's
			// own parts. A non-nil (possibly empty) slice replaces them.
			if tci.Parts != nil {
				results[i].ContentParts = tci.Parts
			}
		}
	}

	return results
}
