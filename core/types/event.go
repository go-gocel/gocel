// ❄️ FROZEN — Stable data contract. Types, fields and semantics must not change.
package types

import "time"

// EventType represents the type of event emitted during agent execution.
//
// EventType 表示 Agent 执行过程中发出的事件类型。
type EventType int

const (
	// EventToken is emitted when a text token is generated.
	// EventToken 当生成文本 token 时触发。
	EventToken EventType = iota
	// EventToolCall is emitted when the model requests a tool call.
	// EventToolCall 当模型请求工具调用时触发。
	EventToolCall
	// EventToolResult is emitted when a tool returns its result.
	// EventToolResult 当工具返回结果时触发。
	EventToolResult
	// EventFinish is emitted when the agent completes successfully.
	// EventFinish 当 Agent 成功完成时触发。
	EventFinish
	// EventError is emitted when an error occurs.
	// EventError 当发生错误时触发。
	EventError
	// EventInterrupt is emitted when execution is paused for human input.
	// EventInterrupt 当等待人工输入暂停执行时触发。
	EventInterrupt
	// EventParseResult is emitted when structured output parsing completes.
	// EventParseResult 当结构化输出解析完成时触发。
	EventParseResult
	// EventState is emitted for state notifications (not model tokens).
	// EventState 用于状态通知。
	EventState
	// EventReasoning is emitted when a reasoning_content token is generated.
	// EventReasoning 当生成 reasoning_content token 时触发。
	EventReasoning
	// EventNotice is emitted when asynchronous background work settles —
	// a subagent finished, a background job ended, or a goal round completed.
	// The payload is carried in Event.Notice.
	//
	// EventNotice 当异步后台工作落定（子代理结束、后台作业结束、
	// goal 轮次完成）时触发。载荷在 Event.Notice 中。
	EventNotice
)

// String returns a human-readable name for the event type.
// String 返回事件类型的人类可读名称。
func (et EventType) String() string {
	switch et {
	case EventToken:
		return "token"
	case EventToolCall:
		return "tool_call"
	case EventToolResult:
		return "tool_result"
	case EventFinish:
		return "finish"
	case EventError:
		return "error"
	case EventInterrupt:
		return "interrupt"
	case EventParseResult:
		return "parse_result"
	case EventState:
		return "state"
	case EventReasoning:
		return "reasoning"
	case EventNotice:
		return "notice"
	default:
		return "unknown"
	}
}

// Event represents a single event emitted during agent execution (token, tool call, error, etc.).
//
// Event 表示 Agent 执行过程中的单个事件（token、工具调用、错误等）。
type Event struct {
	Type       EventType      `json:"type"`
	Content    string         `json:"content,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolArgs   string         `json:"tool_args,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Usage      *TokenUsage    `json:"usage,omitempty"`
	Error      error          `json:"-"`
	Meta       map[string]any `json:"meta,omitempty"`
	// ParseResult carries the output of structured parsing when Type is EventParseResult.
	ParseResult *ParseResult `json:"-"`
	// Notice carries the structured notice payload when Type is EventNotice.
	// Notice 当 Type 为 EventNotice 时携带结构化通知载荷。
	Notice *Notice `json:"notice,omitempty"`
	// RunPath carries the agent hierarchy path where this event originated, e.g. "root/supervisor/agent_name".
	// RunPath 携带事件产生的 Agent 层级路径，格式 "root/supervisor/agent_name"。
	RunPath string `json:"run_path,omitempty"`
}

// NoticeKind names the feature domain that produced a Notice. Feature
// packages own their kind's Meta payload schema; the base Notice carries
// enough for a consumer to render it without importing the feature.
//
// NoticeKind 命名产生 Notice 的特性域。各特性包拥有其 kind 的 Meta 载荷
// schema；基础 Notice 字段足以让消费方渲染，无需引入特性包。
type NoticeKind string

const (
	// NoticeKindSubagent marks a notice about a child agent settling; it is
	// one of the well-known notice kinds. Feature packages may add their own.
	// NoticeKindSubagent 标记子代理落定的通知。它是内置的常用通知种类之一，
	// 特性包可自行扩展更多种类。
	NoticeKindSubagent NoticeKind = "subagent"
	// NoticeKindJob marks a notice about a background job settling.
	// NoticeKindJob 标记后台作业结算的通知。
	NoticeKindJob NoticeKind = "job"
	// NoticeKindGoalRound marks a notice about a goal round completing.
	// NoticeKindGoalRound 标记目标轮次完成的通知。
	NoticeKindGoalRound NoticeKind = "goal_round"
)

// Notice is a structured asynchronous completion notification. It travels as
// an EventNotice event through the SendEvent channel; strategies decide
// whether and how to surface it to the model (by default it stays out of the
// model context).
//
// Notice 是结构化的异步完成通知，经 SendEvent 通道以 EventNotice 事件传递；
// 是否及如何呈现给模型由策略决定（默认不进模型上下文）。
type Notice struct {
	// Kind is the feature domain that produced this notice.
	Kind NoticeKind `json:"kind"`
	// ID identifies the settled work (subagent id, job id, goal id).
	ID string `json:"id"`
	// Status is the feature-defined terminal status, e.g. "complete",
	// "killed", "failed", "blocked".
	Status string `json:"status"`
	// Label is a short human-readable name of the settled work.
	Label string `json:"label,omitempty"`
	// At is the settlement time; consumers order and deduplicate on it.
	At time.Time `json:"at,omitempty"`
	// Err carries a failure message when the work did not complete.
	Err string `json:"error,omitempty"`
	// Meta carries feature-defined payload; never required for rendering.
	Meta map[string]any `json:"meta,omitempty"`
}

// FinishEvent creates a finish event.
// FinishEvent 创建完成事件。
func FinishEvent() *Event {
	return &Event{Type: EventFinish}
}

// ErrorEvent creates an error event.
// ErrorEvent 创建错误事件。
func ErrorEvent(err error) *Event {
	return &Event{Type: EventError, Error: err}
}

// TokenEvent creates a token event.
// TokenEvent 创建 token 事件。
func TokenEvent(content string) *Event {
	return &Event{Type: EventToken, Content: content}
}

// ToolCallEvent creates a tool call event.
// ToolCallEvent 创建工具调用事件。
func ToolCallEvent(name, args, toolCallID string) *Event {
	return &Event{Type: EventToolCall, ToolName: name, ToolArgs: args, ToolCallID: toolCallID}
}

// ToolResultEvent creates a tool result event.
// ToolResultEvent 创建工具结果事件。
func ToolResultEvent(name, result, toolCallID string) *Event {
	return &Event{Type: EventToolResult, ToolName: name, Content: result, ToolCallID: toolCallID}
}

// ParseResult wraps parsed structured output with optional error.
// ParseResult 包装解析后的结构化输出及可能的错误。
type ParseResult struct {
	Raw   string
	Value any
	Error error
}

// ParseResultEvent creates a parse result event.
// ParseResultEvent 创建解析结果事件。
func ParseResultEvent(raw string, val any, err error) *Event {
	pr := &ParseResult{Raw: raw, Value: val, Error: err}
	if err != nil {
		return &Event{Type: EventParseResult, Error: err, ParseResult: pr}
	}
	return &Event{Type: EventParseResult, Content: raw, ParseResult: pr}
}

// ReasoningEvent creates a reasoning content event.
// ReasoningEvent 创建 reasoning_content 事件。
func ReasoningEvent(content string) *Event {
	return &Event{Type: EventReasoning, Content: content}
}

// NoticeEvent creates an EventNotice event carrying a structured notice.
// NoticeEvent 创建携带结构化通知的 EventNotice 事件。
func NoticeEvent(n *Notice) *Event {
	return &Event{Type: EventNotice, Notice: n}
}
