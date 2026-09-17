package runtime

import (
	"crypto/rand"
	"fmt"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

var _ kernel.AgentContext = (*AgentContext)(nil)

// AgentContext implements kernel.AgentContext, holding ephemeral state for a
// single agent execution. The Runner creates one per execution and injects
// it into the context; every module reads run identity, state, events, and
// interrupts from here.
//
// AgentContext 实现 kernel.AgentContext，承载单次 Agent 执行的临时状态。
// Runner 每次执行创建一个并注入 context；所有模块从这里读取
// 运行身份、状态、事件与中断。
type AgentContext struct {
	invocationID    string
	branch          string
	enableStreaming bool
	interruptInput  chan string
	sendEvent       func(event *types.Event) bool
	runPath         string
	contextPassing  string
	parentAgent     kernel.Agent
	agentName       string
	state           kernel.StateManager
	facts           *types.RuntimeFacts
}

// InvocationID returns the unique id of this execution.
// InvocationID 返回本次执行的唯一标识。
func (ac *AgentContext) InvocationID() string                     { return ac.invocationID }
// AgentName returns the name of the running agent.
// AgentName 返回正在运行的 agent 名称。
func (ac *AgentContext) AgentName() string                        { return ac.agentName }
// Branch returns the branch identity of this execution.
// Branch 返回本次执行的分支标识。
func (ac *AgentContext) Branch() string                           { return ac.branch }
// EnableStreaming reports whether streaming mode is enabled.
// EnableStreaming 返回是否启用流式模式。
func (ac *AgentContext) EnableStreaming() bool                    { return ac.enableStreaming }
// InterruptInput returns the channel that carries interrupt input.
// InterruptInput 返回承载中断输入的通道。
func (ac *AgentContext) InterruptInput() chan string              { return ac.interruptInput }
// SendEvent returns the event sender of this execution.
// SendEvent 返回本次执行的事件发送函数。
func (ac *AgentContext) SendEvent() func(event *types.Event) bool { return ac.sendEvent }
// RunPath returns the run path of this execution.
// RunPath 返回本次执行的运行路径。
func (ac *AgentContext) RunPath() string                          { return ac.runPath }
// ContextPassing returns the context passing mode of this execution.
// ContextPassing 返回本次执行的上下文传递模式。
func (ac *AgentContext) ContextPassing() string                   { return ac.contextPassing }
// ParentAgent returns the parent agent used for call-chain tracing.
// ParentAgent 返回用于调用链追踪的父 agent。
func (ac *AgentContext) ParentAgent() kernel.Agent                { return ac.parentAgent }

// State returns the shared StateManager of this execution. It is the same
// instance the Runtime holds — the single source of truth for cross-module
// and cross-agent state during the run.
//
// State 返回本次执行的共享 StateManager，与 Runtime 持有的为同一实例——
// 运行期间跨模块、跨 Agent 状态的单一来源。
func (ac *AgentContext) State() kernel.StateManager {
	if ac == nil {
		return nil
	}
	return ac.state
}

// Facts returns the authoritative runtime snapshot of this execution.
// The Runner fills and refreshes it between steps; modules may update
// individual fields (budget, counters) through the returned pointer.
//
// Facts 返回本次执行的权威运行时快照。Runner 在步间填充并刷新；
// 模块可经返回的指针更新个别字段（预算、计数器）。
func (ac *AgentContext) Facts() *types.RuntimeFacts {
	if ac == nil {
		return &types.RuntimeFacts{}
	}
	if ac.facts == nil {
		ac.facts = &types.RuntimeFacts{}
	}
	return ac.facts
}

// SetSendEvent sets the event sender of this execution.
// SetSendEvent 设置本次执行的事件发送函数。
func (ac *AgentContext) SetSendEvent(fn func(event *types.Event) bool) {
	if ac != nil {
		ac.sendEvent = fn
	}
}

// SetBranch sets the branch identity (resume path) — mechanism method
// increment, consumed by run-path observability and graph resume.
//
// SetBranch 设置分支标识（恢复路径）——机制方法增量，供运行路径观测与图恢复消费。
func (ac *AgentContext) SetBranch(branch string) {
	if ac != nil {
		ac.branch = branch
	}
}

// SetInterruptInput sets the channel that carries interrupt input.
// SetInterruptInput 设置承载中断输入的通道。
func (ac *AgentContext) SetInterruptInput(ch chan string) {
	if ac != nil {
		ac.interruptInput = ch
	}
}

// NewInvocationID generates a unique invocation identifier for tracing.
// NewInvocationID 生成用于追踪的唯一调用标识。
func NewInvocationID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("inv_%x", b)
}

// AgentContextOption configures an AgentContext.
// AgentContextOption 配置 AgentContext。
type AgentContextOption func(*AgentContext)

// WithContextBranch sets the branch identifier.
// WithContextBranch 设置分支标识。
func WithContextBranch(branch string) AgentContextOption {
	return func(ac *AgentContext) { ac.branch = branch }
}

// WithContextEnableStreaming enables streaming mode.
// WithContextEnableStreaming 启用流式模式。
func WithContextEnableStreaming(enabled bool) AgentContextOption {
	return func(ac *AgentContext) { ac.enableStreaming = enabled }
}

// WithContextPassing sets the context passing mode (default "full_dialogue").
// WithContextPassing 设置上下文传递模式（默认 "full_dialogue"）。
func WithContextPassing(passing string) AgentContextOption {
	return func(ac *AgentContext) { ac.contextPassing = passing }
}

// WithParentAgent sets the parent agent for call-chain tracing.
// WithParentAgent 为调用链追踪设置父 agent。
func WithParentAgent(parent kernel.Agent) AgentContextOption {
	return func(ac *AgentContext) { ac.parentAgent = parent }
}

// WithRunPath sets the run path.
// WithRunPath 设置运行路径。
func WithRunPath(path string) AgentContextOption {
	return func(ac *AgentContext) { ac.runPath = path }
}

// WithContextAgentName sets the agent name.
// WithContextAgentName 设置 agent 名称。
func WithContextAgentName(name string) AgentContextOption {
	return func(ac *AgentContext) { ac.agentName = name }
}

// WithContextState attaches the shared state manager (default: the runtime's).
// WithContextState 附加共享状态管理器（默认使用 runtime 的）。
func WithContextState(state kernel.StateManager) AgentContextOption {
	return func(ac *AgentContext) { ac.state = state }
}

// WithContextFacts attaches the initial runtime facts snapshot (default:
// an empty snapshot, filled by the Runner between steps).
// WithContextFacts 附加初始运行时事实快照（默认空快照，由 Runner 步间填充）。
func WithContextFacts(facts *types.RuntimeFacts) AgentContextOption {
	return func(ac *AgentContext) { ac.facts = facts }
}

// NewAgentContext creates a new AgentContext with the given options.
// When no state is attached, a fresh in-memory state is created — the Runner
// normally attaches the runtime's shared state instead.
//
// NewAgentContext 用给定选项创建 AgentContext。
// 未附加状态时创建全新内存状态——Runner 通常会附加 runtime 的共享状态。
func NewAgentContext(opts ...AgentContextOption) *AgentContext {
	ac := &AgentContext{
		invocationID:   NewInvocationID(),
		state:          NewInMemoryState(),
		contextPassing: "full_dialogue",
	}
	for _, opt := range opts {
		opt(ac)
	}
	return ac
}
