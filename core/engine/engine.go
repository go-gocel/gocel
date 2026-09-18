// Package engine defines the execution Engine contract for agent loops.
//
// The Engine is the mechanism that decides HOW an agent loop runs:
// model call → tool execution → observe → repeat. Implementations choose the
// loop strategy (e.g. the ReAct loop in gocel/engine).
//
// The Engine drives the loop, fires step hooks, executes tool calls, and
// produces a *kernel.Result with a termination reason.
//
// Implementations:
//   - StepLoop (gocel/engine): the generic ReAct loop.
//   - Custom: any loop that calls rt.CallModel / rt.ExecTools.
//
// 包 engine 定义 Agent 循环的执行引擎契约。
// Engine 是机制：决定循环怎么跑（模型调用 → 工具执行 → 观察 → 重复）。
// 实现者决定循环策略（如 gocel/engine 的 ReAct 循环）。
// 引擎驱动循环、触发步骤钩子、执行工具调用、产出带终止原因的 *kernel.Result。
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
	//
	// Run 对给定输入执行循环并返回结果。
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

	// InitialGenOpts are the generation options for the model calls.
	//
	// InitialGenOpts 是模型调用的生成选项。
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
