package agents

import (
	"context"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/tools/subagent"
)

// defaultSubAgentPrompt is the built-in instruction set for a delegated child.
//
// defaultSubAgentPrompt 是被委派子代理的内置指令集。
const defaultSubAgentPrompt = `You are a sub-agent working on one delegated task.
Work with the tools you are given; do not ask the caller for clarification.
Your final message is the only thing the caller sees — keep it short and self-contained.
Do not delegate further unless you were given delegation tools.`

// NewSubAgent builds the agent a delegated task runs on. It shares the ReAct
// engine with NewReactAgent and differs in defaults only: the name is
// "subagent", it carries a sub-agent instruction set, and its step budget is
// 10. Tools are injected by the caller — a child normally gets read-only tools
// and MUST NOT get the delegation tools by default, or delegation recurses
// without bound.
//
// NewSubAgent 创建被委派任务运行的子代理。它与 NewReactAgent 共用 ReAct 引擎，
// 只在默认值上不同：名称为 "subagent"、携带子代理指令集、步数预算为 10。
// 工具由调用方注入——子代理通常只拿只读工具，且默认绝不能拿到委派工具，
// 否则委派会无节制递归。
func NewSubAgent(opts ...Option) kernel.Agent {
	return New(append([]Option{
		WithName("subagent"),
		WithSystemPrompt(defaultSubAgentPrompt),
		WithMaxSteps(10),
	}, opts...)...)
}

// DelegationOption configures a Delegation.
// DelegationOption 配置一次委派装配。
type DelegationOption func(*delegationConfig)

// delegationConfig collects DelegationOptions before construction.
type delegationConfig struct {
	factory  func(ctx context.Context) kernel.Agent
	maxDepth int
	history  func(ctx context.Context) []*types.Message
	approval kernel.ApprovalPolicy
}

// WithSubAgentFactory replaces the child-agent factory. The default builds a
// bare NewSubAgent() per spawn; pass this to give children tools, a system
// prompt, or a different engine.
//
// WithSubAgentFactory 替换子代理工厂。默认每次派发构建一个无工具的
// NewSubAgent()；需要给子代理工具、提示词或不同引擎时传入本选项。
func WithSubAgentFactory(f func(ctx context.Context) kernel.Agent) DelegationOption {
	return func(c *delegationConfig) {
		if f != nil {
			c.factory = f
		}
	}
}

// WithDelegationDepth caps the delegation tree depth; a spawn beyond it is
// rejected by the tools. 0 means unlimited.
//
// WithDelegationDepth 限制委派树深度，超出即被工具拒绝。0 表示不限。
func WithDelegationDepth(n int) DelegationOption {
	return func(c *delegationConfig) { c.maxDepth = n }
}

// WithDelegationHistory supplies the conversation history that
// subagent_fork inherits. Without it a fork sends the task alone.
//
// WithDelegationHistory 提供 subagent_fork 继承的对话历史；不设置时
// fork 只发送任务本身。
func WithDelegationHistory(f func(ctx context.Context) []*types.Message) DelegationOption {
	return func(c *delegationConfig) { c.history = f }
}

// WithDelegatedApproval pins an approval policy at the delegation boundary:
// every tool call a child makes is decided by that policy instead of the
// parent's live configuration.
//
// WithDelegatedApproval 把审批策略钉在委派边界上：子代理的每次工具调用都由
// 该策略裁决，而不是继承父级的实时配置。
func WithDelegatedApproval(p kernel.ApprovalPolicy) DelegationOption {
	return func(c *delegationConfig) { c.approval = p }
}

// Delegation is a parent's delegation assembly: one shared child-agent
// registry, the child-agent factory, and the model-facing tools the parent
// exposes. It is the single wiring point between a parent agent and the
// children it spawns — the registry it owns must also be handed to anything
// else that runs children (e.g. the workflow engine).
//
// Delegation 是一次委派装配：一个共享的子代理注册表、子代理工厂，以及父代理
// 暴露给模型的委派工具。它是父代理与它派生的子代理之间唯一的接线点——它持有
// 的注册表也必须交给其他运行子代理的组件（例如工作流引擎）。
type Delegation struct {
	cfg      delegationConfig
	registry *orchestrate.Registry
}

// NewDelegation builds the assembly. The default factory produces a bare
// NewSubAgent() per spawn; inject tools via WithSubAgentFactory.
//
// NewDelegation 创建委派装配。默认工厂每次派发产出一个无工具的
// NewSubAgent()；工具经 WithSubAgentFactory 注入。
func NewDelegation(opts ...DelegationOption) *Delegation {
	cfg := delegationConfig{
		factory: func(context.Context) kernel.Agent { return NewSubAgent() },
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Delegation{cfg: cfg, registry: orchestrate.NewRegistry()}
}

// Registry returns the shared child-agent registry, so callers can hand it to
// the workflow engine or query live children.
//
// Registry 返回共享的子代理注册表，便于交给工作流引擎或查询活体子代理。
func (d *Delegation) Registry() *orchestrate.Registry { return d.registry }

// Agent builds one child agent through the factory.
//
// Agent 通过工厂构建一个子代理实例。
func (d *Delegation) Agent(ctx context.Context) kernel.Agent { return d.cfg.factory(ctx) }

// Tools builds the tools the parent exposes to its model: subagent and
// subagent_fork spawn children, send_message / interrupt_agent / list_agents
// control them afterwards.
//
// Tools 构建父代理暴露给模型的工具：subagent 与 subagent_fork 负责派发，
// send_message / interrupt_agent / list_agents 负责后续控制。
func (d *Delegation) Tools() ([]kernel.Tool, error) {
	return subagent.Tools(subagent.Config{
		Registry:          d.registry,
		Factory:           d.cfg.factory,
		History:           d.cfg.history,
		MaxDepth:          d.cfg.maxDepth,
		DelegatedApproval: d.cfg.approval,
	})
}

// MustTools is like Tools but panics on a configuration error.
//
// MustTools 类似 Tools，配置出错时 panic。
func (d *Delegation) MustTools() []kernel.Tool {
	ts, err := d.Tools()
	if err != nil {
		panic(err)
	}
	return ts
}
