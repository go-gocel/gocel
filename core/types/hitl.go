// ❄️ FROZEN — Stable data contract. Types, fields and semantics must not change.
package types

import "time"

// HITLMode defines the interaction mode for Human-In-The-Loop.
//
// HITLMode 定义人机协作的交互模式。
type HITLMode string

const (
	// HITLModeConfirm is the mode where a human approves or rejects tool calls.
	// HITLModeConfirm 确认模式：人类批准或拒绝工具调用。
	HITLModeConfirm HITLMode = "confirm"
	// HITLModeReview is the mode where a human reviews tool call details before approval.
	// HITLModeReview 审查模式：人类查看工具调用详情后批准。
	HITLModeReview HITLMode = "review"
	// HITLModeSelect is the mode where a human selects from multiple options.
	// HITLModeSelect 选择模式：人类从多个选项中选取。
	HITLModeSelect HITLMode = "select"
)

// HITLStatus represents the current status of a HITL interaction.
//
// HITLStatus 表示 HITL 交互的当前状态。
type HITLStatus string

const (
	// HITLStatusPending is the status while awaiting a human response.
	// HITLStatusPending 等待人类响应中。
	HITLStatusPending  HITLStatus = "pending"   // awaiting human response / 等待人类响应
	// HITLStatusApproved is the status once the human approves.
	// HITLStatusApproved 已由人类批准。
	HITLStatusApproved HITLStatus = "approved"  // approved by human / 已批准
	// HITLStatusRejected is the status once the human rejects.
	// HITLStatusRejected 已由人类拒绝。
	HITLStatusRejected HITLStatus = "rejected"  // rejected by human / 已拒绝
	// HITLStatusModified is the status when the human approves with modified arguments.
	// HITLStatusModified 参数已修改后批准。
	HITLStatusModified HITLStatus = "modified"  // approved with modified args / 参数已修改后批准
	// HITLStatusTimedOut is the status when the wait timed out.
	// HITLStatusTimedOut 等待超时。
	HITLStatusTimedOut HITLStatus = "timed_out" // waiting timed out / 等待超时
)

// HITLInfo carries complete context for a HITL interruption.
// Passed to consumers via EventInterrupt.Meta["hitl_info"].
//
// HITLInfo 携带 HITL 中断的完整上下文信息。
// 通过 EventInterrupt 的 Meta["hitl_info"] 传递给消费者。
type HITLInfo struct {
	Mode         HITLMode   `json:"mode"`
	Status       HITLStatus `json:"status"`
	ToolName     string     `json:"tool_name"`
	ToolArgs     string     `json:"tool_args"`
	AgentName    string     `json:"agent_name"`
	Branch       string     `json:"branch,omitempty"`
	InvocationID string     `json:"invocation_id"`
	SessionID    string     `json:"session_id"`
	Timestamp    time.Time  `json:"timestamp"`
	Options      []string   `json:"options,omitempty"`
	Prompt       string     `json:"prompt,omitempty"`

	// Questions carries a batch of questions when the interruption is a
	// multi-question ask (DSH user-questions batch). Each question is
	// independent: its own options and multi-select flag. When present,
	// Prompt/Options describe the whole batch or are empty.
	//
	// Questions 在中断是一次批量提问时携带问题批次（DSH user-questions
	// 批量）。每个问题独立：各自的选项与多选标记。存在时 Prompt/Options
	// 描述整个批次或为空。
	Questions []HITLQuestion `json:"questions,omitempty"`

	// Intent declares the presentation intent of the interruption (DSH
	// presentation intent): a UI that recognises the tag renders the ask as
	// that kind of decision (e.g. "plan-review" presents Prompt as a plan
	// under review with Approve/Reject). Intents change presentation only —
	// a UI that does not know the tag renders the generic options, and the
	// answer fields are the same either way.
	//
	// Intent 声明中断的呈现意图（DSH presentation intent）：识别该标签的
	// UI 把提问呈现为对应决策类型（如 "plan-review" 把 Prompt 呈现为待
	// 审计划并提供 Approve/Reject）。意图只改变呈现——不识别标签的 UI
	// 仍渲染通用选项，答案字段两种情形相同。
	Intent string `json:"intent,omitempty"`
}

// HITLQuestion is one item of a multi-question ask.
//
// HITLQuestion 是一次批量提问中的单个问题。
type HITLQuestion struct {
	// ID is the stable answer key the human's response is matched against.
	ID string `json:"id"`
	// Question is the question text.
	Question string `json:"question"`
	// Detail carries supporting text rendered with the question without
	// becoming an option label (DSH detail).
	Detail string `json:"detail,omitempty"`
	// Header is an optional short heading for the question.
	Header string `json:"header,omitempty"`
	// Options is the optional choice list; empty = free text answer.
	Options []string `json:"options,omitempty"`
	// MultiSelect allows choosing several options at once.
	MultiSelect bool `json:"multi_select,omitempty"`
}

// HITLAnswer is the human's answer to one HITLQuestion.
//
// HITLAnswer 是人类对一个 HITLQuestion 的回答。
type HITLAnswer struct {
	// ID echoes the question's stable id.
	ID string `json:"id"`
	// Selected holds the chosen option labels (one for single-select, many
	// for multi-select).
	Selected []string `json:"selected,omitempty"`
	// Custom carries free text overriding the selection (single-select).
	Custom string `json:"custom,omitempty"`
}

// HITLDecision represents the human's response decision to a HITL interruption.
//
// HITLDecision 表示人类对 HITL 中断的响应决策。
type HITLDecision struct {
	Approved       bool   `json:"approved"`
	Skip           bool   `json:"skip,omitempty"`
	ModifiedArgs   string `json:"modified_args,omitempty"`
	Feedback       string `json:"feedback,omitempty"`
	SelectedOption string `json:"selected_option,omitempty"`
	// Answers carries the batch answers when the interruption was a
	// multi-question ask. Each entry echoes its question id.
	//
	// Answers 在中断是批量提问时携带批量答案。每条回显其问题 id。
	Answers []HITLAnswer `json:"answers,omitempty"`
}

// NewHITLInfo creates a HITLInfo instance with pending status.
//
// NewHITLInfo 创建一个状态为 pending 的 HITLInfo 实例。
func NewHITLInfo(mode HITLMode, toolName, toolArgs string) *HITLInfo {
	return &HITLInfo{
		Mode:      mode,
		Status:    HITLStatusPending,
		ToolName:  toolName,
		ToolArgs:  toolArgs,
		Timestamp: time.Now(),
	}
}

// IsFinal checks if the decision is a final state (approved/rejected/timeout).
//
// IsFinal 判断决策是否为最终状态（批准、拒绝或超时）。
func (d *HITLDecision) IsFinal() bool {
	return d.Approved || d.Skip
}

// ToInterruptEvent converts HITLInfo to an EventInterrupt event.
//
// ToInterruptEvent 将 HITLInfo 转换为 EventInterrupt 事件。
func (info *HITLInfo) ToInterruptEvent() *Event {
	evt := &Event{
		Type:     EventInterrupt,
		ToolName: info.ToolName,
		Content:  info.ToolArgs,
		Meta: map[string]any{
			"hitl_info": info,
			"hitl_mode": string(info.Mode),
		},
	}
	return evt
}
