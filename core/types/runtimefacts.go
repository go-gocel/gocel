package types

// PermissionMode is the file-effect policy tier of a session. It bounds what
// a tool may do to the filesystem; enforcement lives in the FilePolicy
// implementation, and the model learns the current tier through RuntimeFacts.
//
// PermissionMode 是会话的文件操作权限档位：约束工具对文件系统能做什么，
// 由 FilePolicy 实现执行，模型经 RuntimeFacts 感知当前档位。
type PermissionMode int

const (
	// PermissionReadOnly allows reads and searches only. It is the zero
	// value: an uninitialized permission mode fails closed.
	//
	// PermissionReadOnly 只允许读取与检索。它是零值：未初始化的权限档
	// fail-closed。
	PermissionReadOnly PermissionMode = iota
	// PermissionWorkspaceWrite additionally allows writes inside the
	// workspace root. It is the default for fresh sessions.
	// PermissionWorkspaceWrite 额外允许工作区根目录内的写入（新会话默认档）。
	PermissionWorkspaceWrite
	// PermissionDangerFullAccess allows writes and execution anywhere.
	// PermissionDangerFullAccess 允许任意位置的写入与执行。
	PermissionDangerFullAccess
)

// String returns the machine-readable name of the permission mode.
// String 返回权限档位的机器可读名称。
func (m PermissionMode) String() string {
	switch m {
	case PermissionReadOnly:
		return "read-only"
	case PermissionWorkspaceWrite:
		return "workspace-write"
	case PermissionDangerFullAccess:
		return "danger-full-access"
	default:
		return "unknown"
	}
}

// AtLeast reports whether m is at least as permissive as n on the strict
// escalation ladder: read-only < workspace-write < danger-full-access.
// Escalation must always move up this ladder — a request to widen by less
// than the current tier is rejected by callers, mirroring DSH's WIDER_MODES.
//
// AtLeast 报告 m 在严格升级阶梯上是否至少与 n 同等宽松：
// read-only < workspace-write < danger-full-access。升级必须沿阶梯向上——
// 宽于当前档的请求被调用方拒绝，对应 DSH 的 WIDER_MODES。
func (m PermissionMode) AtLeast(n PermissionMode) bool {
	return m >= n
}

// FileOp classifies a file-effect operation for policy checks.
// FileOp 对文件操作分类，供策略检查。
type FileOp int

const (
	// FileOpRead is reading or searching file content or metadata.
	// FileOpRead 表示读取或检索文件内容或元数据。
	FileOpRead FileOp = iota
	// FileOpWrite is creating, modifying, moving, or deleting.
	// FileOpWrite 表示创建、修改、移动或删除。
	FileOpWrite
	// FileOpExec is launching programs or executing scripts/shell commands.
	// FileOpExec 表示启动程序或执行脚本/shell 命令。
	FileOpExec
)

// String returns the machine-readable name of the file operation.
// String 返回文件操作的机器可读名称。
func (op FileOp) String() string {
	switch op {
	case FileOpRead:
		return "read"
	case FileOpWrite:
		return "write"
	case FileOpExec:
		return "exec"
	default:
		return "unknown"
	}
}

// SessionMode is the session-level interaction mode. It gates mutation
// capability at the session level, independent of the permission tier:
// plan mode keeps the tool catalog stable while denying every write/exec.
//
// SessionMode 是会话级交互模式：在权限档位之外，对"能否修改"做会话级门控。
// plan 模式保持工具目录不变但拒绝一切写入/执行。
type SessionMode int

const (
	// SessionModeNormal is the ordinary mode where the agent executes work.
	// It is the zero value: the default session mode.
	//
	// SessionModeNormal 是普通模式：Agent 执行工作。它是零值——默认会话模式。
	SessionModeNormal SessionMode = iota
	// SessionModePlan is the read-only planning mode: the agent explores and
	// proposes; mutation is gated until the session returns to normal mode.
	// SessionModePlan 是只读计划模式：Agent 探索并提出方案；在会话回到
	// 普通模式前禁止一切变更。
	SessionModePlan
)

// String returns the machine-readable name of the session mode.
// String 返回会话模式的机器可读名称。
func (m SessionMode) String() string {
	switch m {
	case SessionModeNormal:
		return "normal"
	case SessionModePlan:
		return "plan"
	default:
		return "unknown"
	}
}

// RuntimeFacts is the authoritative snapshot of the runtime environment,
// injected once per execution so the model reasons about its own situation
// (working directory, permission tier, budget) without hallucinating.
//
// Each injected snapshot SUPERSEDES earlier ones: renderers replace the
// previous block instead of accumulating, so the model context carries
// exactly one authoritative picture per run.
//
// RuntimeFacts 是运行时环境的权威快照，每次执行注入一次，
// 让模型对自身处境（工作目录、权限档位、预算）的推理有据可依。
//
// 每次注入的快照取代（supersede）此前的快照：渲染器替换而非累积，
// 模型上下文中每次运行只有一份权威画面。
type RuntimeFacts struct {
	// CWD is the working directory of the session.
	CWD string `json:"cwd,omitempty"`
	// WorkspaceRoot is the workspace root the permission tier is relative to.
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	// Permission is the session's current permission tier.
	Permission PermissionMode `json:"permission"`
	// Approval is the approval disposition the session runs under, one of
	// "ask", "auto", "deny" (or empty when unknown). It tells the model
	// whether its tool calls will require human approval.
	//
	// Approval 是会话运行的审批处置："ask"/"auto"/"deny"（未知时为空）。
	// 它告诉模型其工具调用是否需要人工审批。
	Approval string `json:"approval,omitempty"`
	// Mode is the session interaction mode (normal or plan).
	Mode SessionMode `json:"mode"`
	// Model is the display name of the model serving this session.
	Model string `json:"model,omitempty"`
	// SessionID identifies the persisted session, when one exists.
	SessionID string `json:"session_id,omitempty"`
	// TokenBudget is the configured context budget in tokens; 0 = unknown.
	TokenBudget int `json:"token_budget,omitempty"`
	// TokensUsed is the estimated tokens consumed so far; 0 = unknown.
	TokensUsed int `json:"tokens_used,omitempty"`
	// GoalSummary is a one-line summary of the active session goal, if any.
	GoalSummary string `json:"goal_summary,omitempty"`
	// ActiveSubagents counts currently running child agents.
	ActiveSubagents int `json:"active_subagents,omitempty"`
	// ActiveJobs counts currently running background jobs.
	ActiveJobs int `json:"active_jobs,omitempty"`
	// SessionLogSeq is the last-committed sequence number of the session's
	// event log (0 when the session has no log or none is mounted). It is
	// the replay/subscription watermark for consumers that read session
	// history incrementally — a GUI replay buffer, a projection store, or a
	// log export can resume from this position instead of rescanning.
	//
	// SessionLogSeq 是会话事件日志最近一次已提交的序号（无日志或未挂载
	// 时为 0）。它是消费方增量读取会话历史的回放/订阅水位——GUI 重放缓冲、
	// 投影存储或日志导出可从该位置续读而无需重扫。
	SessionLogSeq int64 `json:"session_log_seq,omitempty"`
	// Extra carries feature-specific facts without schema churn.
	Extra map[string]any `json:"extra,omitempty"`
}
