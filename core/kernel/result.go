package kernel

import "github.com/go-gocel/gocel/core/types"

// ❄️ FROZEN — Stable type. Fields and semantics must not change.
//
// TerminateReason explains why an agent execution ended. The engine sets it;
// consumers use it to distinguish success from budget/cancellation outcomes.
//
// TerminateReason 解释 Agent 执行结束的原因。引擎设置它；
// 消费方用它区分成功与预算/取消等结果。
type TerminateReason int

const (
	// TerminateUnknown is the zero value; engines always set a concrete reason.
	// TerminateUnknown 是零值；引擎总是设置具体原因。
	TerminateUnknown TerminateReason = iota
	// TerminateFinished means the policy decided the task is done.
	// TerminateFinished 表示策略判定任务已完成。
	TerminateFinished
	// TerminateMaxSteps means the step budget was exhausted.
	// TerminateMaxSteps 表示步骤预算已耗尽。
	TerminateMaxSteps
	// TerminateAborted means the policy aborted with an error.
	// TerminateAborted 表示策略因错误中止。
	TerminateAborted
	// TerminateCanceled means the context was canceled (or timed out).
	// TerminateCanceled 表示上下文被取消（或超时）。
	TerminateCanceled
	// TerminateError means a runtime error (model/tool/hook failure) ended the run.
	// TerminateError 表示运行时错误（模型/工具/钩子失败）导致运行结束。
	TerminateError
)

// String returns a human-readable name for the reason.
// String 返回原因的人类可读名称。
func (r TerminateReason) String() string {
	switch r {
	case TerminateFinished:
		return "finished"
	case TerminateMaxSteps:
		return "max_steps"
	case TerminateAborted:
		return "aborted"
	case TerminateCanceled:
		return "canceled"
	case TerminateError:
		return "error"
	default:
		return "unknown"
	}
}

// ❄️ FROZEN — Stable type. Fields and semantics must not change.
//
// Result is the synchronous result of an Agent execution.
// Agent.Run returns *Result—it blocks until execution completes.
//
// Result 是 Agent 同步执行的结果。Agent.Run 返回 *Result，阻塞直到执行完成。
type Result struct {
	// Content is the final text output of the agent.
	// Content 是 Agent 的最终文本输出。
	Content string
	// Messages contains the full conversation trace (for continued conversation or debugging).
	// Messages 包含完整的对话轨迹（用于继续对话或调试）。
	Messages []*types.Message
	// TokenUsage is the total token consumption.
	// TokenUsage 是总 Token 消耗。
	TokenUsage *types.TokenUsage
	// Reason is the termination reason set by the engine.
	// Reason 是引擎设置的终止原因。
	Reason TerminateReason
	// Err is the execution error, if any.
	// Err 是执行错误（如有）。
	Err error
}

// IsSuccess returns true if the execution completed without error.
//
// IsSuccess 返回执行是否成功完成。
func (r *Result) IsSuccess() bool {
	return r != nil && r.Err == nil
}

// IsFailure returns true if the execution ended with an error.
//
// IsFailure 返回执行是否出错。
func (r *Result) IsFailure() bool {
	return r != nil && r.Err != nil
}

// LastMessage returns the last message in the conversation trace.
// Returns nil if there are no messages.
//
// LastMessage 返回对话轨迹的最后一条消息。没有消息时返回 nil。
func (r *Result) LastMessage() *types.Message {
	if r == nil || len(r.Messages) == 0 {
		return nil
	}
	return r.Messages[len(r.Messages)-1]
}
