// ❄️ FROZEN — Stable data contract. Types, fields and semantics must not change.
package types

// AgentInput is the input to an Agent's Run method, containing messages, system prompt, and options.
//
// AgentInput 是 Agent Run 方法的输入，包含消息、系统提示词和选项。
type AgentInput struct {
	Messages        []*Message
	SystemPrompt    string
	EnableStreaming bool
	MaxSteps        int
	Meta            map[string]any
	// InterruptInput is an optional channel for human-in-the-loop input.
	// When set, the agent will send interrupt events and wait for responses on this channel.
	//
	// InterruptInput 可选的 HITL 输入通道。设置后 Agent 将发送中断事件并等待响应。
	InterruptInput chan string
	// StreamSender is set by Runner.Stream() to enable per-token streaming.
	// When non-nil, StepLoop-based agents should use streaming model calls
	// and send per-chunk events through this function.
	// Streaming agents should also set EnableStreaming = true to signal
	// Runner.Stream() not to emit duplicate final events.
	//
	// StreamSender 由 Runner.Stream() 设置以启用逐 Token 流式输出。
	// 非 nil 时，基于 StepLoop 的 Agent 应使用流式模型调用并通过此函数发送逐块事件。
	StreamSender func(*Event) bool
}

// TrimReport reports the result of context trimming.
//
// TrimReport 报告上下文修剪结果。
type TrimReport struct {
	Truncated    bool
	OriginalSize int
	FinalSize    int
	Strategy     string // "compact" | "summary" | "window" | "aggressive"
}
