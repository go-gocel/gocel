package kernel

import (
	"context"

	"github.com/go-gocel/gocel/core/types"
)

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// AgentContext is the per-execution execution context, created and injected
// by the Runner into the context before Agent.Run. It is the single channel
// through which a run's identity, state, events, and interrupts flow.
// Session, memory, HITL, checkpointing, and observability modules all read
// from it — nothing should invent its own context-passing mechanism.
//
// AgentContext 是单次执行的执行上下文，由 Runner 创建并注入 context。
// 它是执行身份、状态、事件与中断的唯一通道。会话、记忆、HITL、
// 检查点、可观测性模块都从它读取——任何模块不得自建上下文传递机制。
type AgentContext interface {
	// InvocationID returns the unique invocation identifier for this execution.
	// InvocationID 返回本次执行的唯一标识。
	InvocationID() string

	// AgentName returns the name of the agent this execution belongs to.
	// AgentName 返回本次执行所属 Agent 的名称。
	AgentName() string

	// Branch returns the branch identifier for checkpointing.
	// Branch 返回检查点用的分支标识。
	Branch() string

	// RunPath returns the agent's run path in a composite/hierarchical setup.
	// RunPath 返回组合/层级场景中的运行路径。
	RunPath() string

	// ContextPassing returns the context passing mode (e.g. "full_dialogue").
	// ContextPassing 返回上下文传递模式。
	ContextPassing() string

	// EnableStreaming returns whether streaming mode is enabled.
	// EnableStreaming 返回是否启用流式模式。
	EnableStreaming() bool

	// InterruptInput returns the channel for receiving interrupt/human input.
	// InterruptInput 返回接收中断/人工输入的通道。
	InterruptInput() chan string

	// SendEvent returns the function for pushing events to the consumer.
	// SendEvent 返回向消费方推送事件的函数。
	SendEvent() func(event *types.Event) bool

	// ParentAgent returns the parent agent in a composite/hierarchical setup.
	// ParentAgent 返回组合/层级场景中的父 Agent。
	ParentAgent() Agent

	// State returns the run's shared state manager — the single source of
	// truth for cross-module, cross-agent state during this execution.
	//
	// State 返回本次运行的共享状态管理器——执行期间跨模块、
	// 跨 Agent 状态的单一来源。
	State() StateManager

	// Facts returns the authoritative runtime snapshot of this execution.
	// The Runner fills the fields it knows (CWD) at run start; permission
	// tier, budget, and counters are filled by product modules between
	// steps. Never nil — implementations must return an empty snapshot at
	// worst.
	//
	// Facts 返回本次执行的权威运行时快照。Runner 在运行开始时填充它
	// 知道的字段（CWD）；权限档位、预算与计数器由产品模块在步间填充。
	// 永不为 nil——实现最差返回空快照。
	Facts() *types.RuntimeFacts

	// SetSendEvent sets the function used to push events to the consumer.
	// Must be called before Run.
	//
	// SetSendEvent 设置向消费方推送事件的函数。必须在 Run 前调用。
	SetSendEvent(fn func(event *types.Event) bool)

	// SetInterruptInput sets the channel for receiving interrupt input.
	// Must be called before Run.
	//
	// SetInterruptInput 设置接收中断输入的通道。必须在 Run 前调用。
	SetInterruptInput(ch chan string)
}

type agentCtxKey struct{}

// WithAgentContext injects an AgentContext into the parent context.
// WithAgentContext 将 AgentContext 注入父 context。
func WithAgentContext(parent context.Context, ac AgentContext) context.Context {
	return context.WithValue(parent, agentCtxKey{}, ac)
}

// GetAgentContext extracts AgentContext from the context. Returns nil if not found.
// GetAgentContext 从 context 中提取 AgentContext；未找到时返回 nil。
func GetAgentContext(ctx context.Context) AgentContext {
	if ac, ok := ctx.Value(agentCtxKey{}).(AgentContext); ok {
		return ac
	}
	return nil
}

// MustAgentContext extracts AgentContext, panicking if not found.
// MustAgentContext 从 context 中提取 AgentContext，未找到时 panic。
func MustAgentContext(ctx context.Context) AgentContext {
	ac := GetAgentContext(ctx)
	if ac == nil {
		panic("core: AgentContext not found in context; use WithAgentContext to inject")
	}
	return ac
}

// ── Runtime context ────────────────────────────────────────────────────

type runtimeCtxKey struct{}

// WithRuntime stores a Runtime in the context for tools and sub-agents.
// WithRuntime 将 Runtime 存入 context，供工具与子 Agent 使用。
func WithRuntime(parent context.Context, rt Runtime) context.Context {
	return context.WithValue(parent, runtimeCtxKey{}, rt)
}

// RuntimeFromContext extracts a Runtime from the context. Returns nil if not found.
// RuntimeFromContext 从 context 中提取 Runtime；未找到时返回 nil。
func RuntimeFromContext(ctx context.Context) Runtime {
	rt, _ := ctx.Value(runtimeCtxKey{}).(Runtime)
	return rt
}

// ── Step index (execution cursor) ──────────────────────────────────────
//
// 步骤游标：单一来源是引擎的循环计数。Runtime 在 FireStepStart 时把它注入
// 返回的 context，作为模型/工具钩子在同一 step 内读取当前步的传递通道。

type stepIndexKey struct{}

// WithStepIndex injects the current step index into the context.
// WithStepIndex 将当前步骤序号注入 context。
func WithStepIndex(parent context.Context, n int) context.Context {
	return context.WithValue(parent, stepIndexKey{}, n)
}

// StepIndexFromContext extracts the current step index. The bool is false
// when no step is in progress (e.g. direct model calls outside a step loop).
//
// StepIndexFromContext 提取当前步骤序号；不在 step 循环内时返回 false。
func StepIndexFromContext(ctx context.Context) (int, bool) {
	n, ok := ctx.Value(stepIndexKey{}).(int)
	return n, ok
}
