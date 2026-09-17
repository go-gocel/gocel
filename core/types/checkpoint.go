// ❄️ FROZEN — Stable data contract. Types, fields and semantics must not change.
package types

import "time"

// Checkpoint stores agent execution state at interrupt points for pause/resume.
// State carries the serialized policy session so resume continues from the
// saved step instead of replaying messages from scratch.
//
// Checkpoint 在中断点保存 Agent 执行状态，支持暂停/恢复。
// State 携带序列化的策略会话，使恢复从保存的步骤继续而非从头重放。
type Checkpoint struct {
	ID              string     `json:"id"`
	AgentName       string     `json:"agent_name"`
	SessionID       string     `json:"session_id"`
	Messages        []*Message `json:"messages"`
	SystemPrompt    string     `json:"system_prompt,omitempty"`
	EnableStreaming bool       `json:"enable_streaming,omitempty"`
	MaxSteps        int        `json:"max_steps,omitempty"`
	StepIndex       int        `json:"step_index,omitempty"`
	// State is the serialized policy session snapshot (json.RawMessage).
	// State 是序列化的策略会话快照。
	State []byte `json:"state,omitempty"`
	// Branch identifies the checkpoint's branch (e.g. graph resume).
	// Branch 标识检查点所属分支（如图恢复）。
	Branch    string         `json:"branch,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// CheckpointID generates a unique checkpoint ID.
// CheckpointID 生成唯一的检查点 ID。
func CheckpointID() string {
	return "ckpt_" + SessionID()
}
