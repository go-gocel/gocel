// Package engine defines the execution Engine contract for agent loops.
//
// The Engine is the MECHANISM that decides HOW an agent loop runs:
// model call → tool execution → observe → repeat. It is strategy-agnostic:
// WHAT to do at each step is decided by the injected kernel.Policy.
//
// The Engine drives the loop, fires step hooks, executes tool calls, and
// produces a *kernel.Result with a termination reason. The engine also
// attaches the policy session snapshot to StepInfo so checkpoint modules
// can persist exact resume points.
//
// Implementations:
//   - StepLoop (gocel/engine): the generic agentic loop.
//   - Custom: any loop that calls rt.CallModel / rt.ExecTools and consults
//     the injected Policy.
//
// 包 engine 定义 Agent 循环的执行引擎契约。
// Engine 是"机制"：决定循环怎么跑（模型调用 → 工具执行 → 观察 → 重复），
// 引擎驱动循环、触发步骤钩子、执行工具调用、产出带终止原因的 *kernel.Result，
// 并把策略会话快照附加到 StepInfo 供检查点模块持久化精确断点。
package engine

import (
	"context"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// Engine executes the core loop that drives an agent.
//
// Engine 是执行引擎契约，实现者决定"怎么跑循环"。
type Engine interface {
	// Run executes the loop over the given input and returns the result.
	// The input carries the Policy (what to do) and optionally a restored
	// PolicySession (resume). If Session is nil, the engine creates one via
	// Policy.NewSession.
	//
	// Run 对给定输入执行循环并返回结果。输入携带 Policy（做什么）与
	// 可选的已恢复 PolicySession（resume）。Session 为 nil 时引擎通过
	// Policy.NewSession 创建。
	Run(ctx context.Context, input *RunInput, rt kernel.Runtime) *kernel.Result
}

// ❄️ FROZEN — Stable type. Fields and semantics must not change.
//
// RunInput is the engine's input for one execution.
// RunInput 是引擎单次执行的输入。
type RunInput struct {
	// AgentName identifies the agent for step hooks and events.
	// AgentName 标识 Agent，用于步骤钩子与事件。
	AgentName string

	// Messages is the full built message list (system + user + strategy
	// instructions), already passed through the OnMessagesBuilt hook.
	//
	// Messages 是构建完成的完整消息列表，已通过 OnMessagesBuilt 钩子。
	Messages []*types.Message

	// InitialGenOpts are the generation options for the first model call,
	// taken from Policy.InitialGenOpts when Session is nil (fresh run).
	//
	// InitialGenOpts 是首次模型调用的生成选项；Session 为 nil（全新执行）
	// 时取自 Policy.InitialGenOpts。
	InitialGenOpts []kernel.GenOption

	// MaxSteps is the step budget (<= 0 falls back to the engine default).
	// MaxSteps 是步数预算（<= 0 时使用引擎默认值）。
	MaxSteps int

	// StreamMode enables per-token streaming via Runtime.CallModelStream.
	// Requires SendEvent to be non-nil.
	//
	// StreamMode 启用逐 token 流式模型调用。要求 SendEvent 非 nil。
	StreamMode bool

	// SendEvent is the optional event emitter. If nil, no events are emitted.
	// SendEvent 是可选的事件发射器。nil 时不发射事件。
	SendEvent func(*types.Event) bool
}
