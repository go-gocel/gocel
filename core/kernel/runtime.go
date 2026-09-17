// Package kernel defines all core interfaces for gocel.
package kernel

import (
	"context"

	"github.com/go-gocel/gocel/core/types"
)

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// ModelExecutor is the interface agents use to interact with models and tools.
// It is the subset of Runtime that Agent implementations actually consume.
//
// ModelExecutor 是 Agent 用于与模型和工具交互的接口，是 Runtime 的子集。
type ModelExecutor interface {
	CallModel(ctx context.Context, msgs []*types.Message, opts ...GenOption) (*types.Message, *types.TokenUsage, error)
	CallModelStream(ctx context.Context, msgs []*types.Message, opts ...GenOption) (StreamReader, error)
	ExecTools(ctx context.Context, toolCalls []*types.ToolCall) []*types.Message
	CountTokens(ctx context.Context, msgs []*types.Message, opts ...GenOption) (int, error)
	ListTools(ctx context.Context) []Tool
}

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// Runtime is the full execution environment passed to Agent.Run. It extends
// ModelExecutor with lifecycle control for engines and the shared run state.
//
// Runtime 是传递给 Agent.Run 的完整执行环境。它扩展了 ModelExecutor，
// 为引擎提供生命周期控制与共享运行状态。
type Runtime interface {
	ModelExecutor

	// Register adds all tools from a ToolProvider to the runtime.
	// For single tools (FuncTool, AgentTool), the tool is registered under its own name.
	// For multi-tool sources (Skill, MCPSource), tools are flattened and
	// prefixed with the provider name to avoid conflicts (e.g. "skill:X/toolY").
	// Tools take effect on the next CallModel/ExecTools immediately.
	//
	// Register 从 ToolProvider 注册工具。单个工具直接注册，多工具源自动展平加前缀。
	Register(ctx context.Context, provider ToolProvider) error

	// Unregister removes a tool by name. Works for any tool regardless of
	// how it was registered (directly or flattened from a provider).
	//
	// Unregister 按名称移除工具。
	Unregister(ctx context.Context, name string) error

	// State returns the shared state manager of this runtime — the single
	// source of truth for cross-module and cross-agent state.
	//
	// State 返回本 runtime 的共享状态管理器——跨模块、跨 Agent 状态的单一来源。
	State() StateManager

	// FireAgentStart fires HookAgentStart. The Runner calls it once before
	// Agent.Run. Returns (ctx, info, error).
	//
	// FireAgentStart 触发 AgentStart 钩子。Runner 在 Agent.Run 前调用一次。
	FireAgentStart(ctx context.Context, info *AgentRunInfo) (context.Context, *AgentRunInfo, error)

	// FireAgentEnd fires HookAgentEnd. The Runner calls it once after the
	// agent run completes.
	//
	// FireAgentEnd 触发 AgentEnd 钩子。Runner 在 Agent 执行完成后调用一次。
	FireAgentEnd(ctx context.Context, info *RunInfo) (context.Context, *RunInfo, error)

	// FireMessagesBuilt fires HookMessagesBuilt after the full message list
	// has been built, once per run. Assembled agents call this before
	// starting their engine.
	//
	// FireMessagesBuilt 在完整消息列表构建完成后触发一次。
	// 装配产物 Agent 在启动引擎前调用。
	FireMessagesBuilt(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error)

	// FireStepStart fires HookStepStart. Engines call this before each
	// iteration. Returns (ctx, continue, info, error). If continue is false,
	// the engine aborts.
	//
	// FireStepStart 触发 StepStart 钩子。引擎在每次迭代前调用。
	FireStepStart(ctx context.Context, info *StepInfo) (context.Context, bool, *StepInfo, error)

	// FireStepEnd fires HookStepEnd. Engines call this after each iteration.
	// Returns (ctx, continue, info, error).
	//
	// FireStepEnd 触发 StepEnd 钩子。引擎在每次迭代后调用。
	FireStepEnd(ctx context.Context, info *StepInfo) (context.Context, bool, *StepInfo, error)

	// FireDecision fires all registered Decision hooks. Guard/approval modules
	// call it when they make a decision (block/approve/reject) about a tool
	// call; audit and observability subscribe via OnDecision.
	//
	// FireDecision 触发所有已注册的 Decision 钩子。守卫/审批模块在
	// 对工具调用作出决策（阻断/批准/拒绝）时调用。
	FireDecision(ctx context.Context, info *DecisionInfo) error
}
