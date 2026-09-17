// ❄️ FROZEN — Stable data contract. Types, fields and semantics must not
// change. This file adds the session-log event vocabulary to the types
// package; it is additive and does not alter any existing type.
package types

import "time"

// SessionEventKind classifies one session-log record. The vocabulary is
// open: feature packages extend it with namespaced kinds (e.g.
// "goal/change", "plan/mode") and own their Meta payload schema — the Go
// counterpart of DSH's merge-extensible SessionEventMap. Kinds are
// lowercase dotted strings; the core kinds below are the shipped baseline.
//
// SessionEventKind 对一条会话日志记录分类。词汇表开放：特性包以命名空间
// 前缀扩展（如 "goal/change"、"plan/mode"）并拥有各自的 Meta 载荷
// schema——对应 DSH 可合并扩展的 SessionEventMap。核心种类如下。
type SessionEventKind string

const (
	// SessionEventUserMessage records a user message entering history.
	// SessionEventUserMessage 记录一条进入历史的用户消息。
	SessionEventUserMessage SessionEventKind = "user/message"
	// SessionEventAssistantMessage records an assistant message.
	// SessionEventAssistantMessage 记录一条助手消息。
	SessionEventAssistantMessage SessionEventKind = "assistant/message"
	// SessionEventToolCall records the model requesting a tool call.
	// SessionEventToolCall 记录模型请求工具调用。
	SessionEventToolCall SessionEventKind = "tool/call"
	// SessionEventToolResult records a tool result.
	// SessionEventToolResult 记录一条工具结果。
	SessionEventToolResult SessionEventKind = "tool/result"
	// SessionEventSystem records a system/instruction message.
	// SessionEventSystem 记录一条系统/指令消息。
	SessionEventSystem SessionEventKind = "system/message"
	// SessionEventNotice records an asynchronous settlement notice.
	// SessionEventNotice 记录一条异步落定通知。
	SessionEventNotice SessionEventKind = "notice"
	// SessionEventCheckpoint records a durable checkpoint marker; it is
	// log-only by default (never joins derived history).
	// SessionEventCheckpoint 记录持久化检查点标记；默认仅入日志（不进入派生历史）。
	SessionEventCheckpoint SessionEventKind = "checkpoint"
)

// SessionSurfaceOp marks how a record enters the derived message history
// (DSH surfaceOp semantics). Compaction and pruners append a replacement
// record that shadows a range of earlier records instead of deleting them —
// the log stays append-only and replay-safe.
//
// SessionSurfaceOp 标记记录如何进入派生消息历史（DSH surfaceOp 语义）。
// 压缩与剪枝追加一条替换记录遮蔽一段更早记录而非删除——日志保持只追加、
// 可重放。
type SessionSurfaceOp int

const (
	// SessionSurfaceAppend adds the record at the end of derived history.
	// 追加：记录进入派生历史末尾。
	SessionSurfaceAppend SessionSurfaceOp = iota
	// SessionSurfaceReplace shadows [SourceFrom, SourceTo] (inclusive)
	// replacing them with this record's message. The shadowed records stay
	// in the log.
	//
	// 替换：以本记录的消息遮蔽 [SourceFrom, SourceTo]（含端点）。
	// 被遮蔽的记录仍留在日志中。
	SessionSurfaceReplace
	// SessionSurfaceLogOnly keeps the record out of derived history
	// entirely (checkpoints, telemetry, bookkeeping).
	//
	// 仅日志：记录完全不进入派生历史（检查点、遥测、记账）。
	SessionSurfaceLogOnly
)

// String returns the machine-readable name of the surface op.
// String 返回 surface 操作的机器可读名称。
func (op SessionSurfaceOp) String() string {
	switch op {
	case SessionSurfaceAppend:
		return "append"
	case SessionSurfaceReplace:
		return "replace"
	case SessionSurfaceLogOnly:
		return "log-only"
	default:
		return "unknown"
	}
}

// SessionEvent is one immutable record of a session log. Seq is the
// monotonic, contiguous sequence number assigned by the log on commit; it
// is the authoritative ordering and replay watermark (RuntimeFacts.
// SessionLogSeq exposes the last committed seq). Message carries the
// model-visible payload for message kinds; Meta carries feature-specific
// payloads (tool results, checkpoint state, goal snapshots) without schema
// churn.
//
// SessionEvent 是会话日志的一条不可变记录。Seq 是日志提交时分配的单调
// 连续序号——是权威排序与回放水位（RuntimeFacts.SessionLogSeq 暴露最近
// 已提交序号）。Message 携带消息类事件的模型可见载荷；Meta 携带特性
// 载荷（工具结果、检查点状态、目标快照）而无需 schema 变动。
type SessionEvent struct {
	// Seq is the monotonic contiguous sequence number (assigned by the log).
	Seq int64 `json:"seq"`
	// Kind classifies the record.
	Kind SessionEventKind `json:"kind"`
	// Surface marks how the record enters derived history.
	Surface SessionSurfaceOp `json:"surface"`
	// SourceFrom is the inclusive start of the shadowed range for
	// SessionSurfaceReplace; 0 otherwise.
	SourceFrom int64 `json:"source_from,omitempty"`
	// SourceTo is the inclusive end of the shadowed range for
	// SessionSurfaceReplace; 0 otherwise.
	SourceTo int64 `json:"source_to,omitempty"`
	// Message is the model-visible payload for message kinds.
	Message *Message `json:"message,omitempty"`
	// Meta carries feature-specific payload (kind-owned schema).
	Meta map[string]any `json:"meta,omitempty"`
	// At is the commit time of the record.
	At time.Time `json:"at"`
}

// NewSessionEvent builds an append-surface record. The Seq field is
// assigned by the log on commit; pass 0 and read the committed copy.
//
// NewSessionEvent 构建追加面记录。Seq 由日志在提交时分配；传入 0 并以
// 提交后的副本为准。
func NewSessionEvent(kind SessionEventKind, msg *Message) SessionEvent {
	return SessionEvent{
		Kind:    kind,
		Surface: SessionSurfaceAppend,
		Message: msg,
		At:      time.Now().UTC(),
	}
}

// NewReplaceEvent builds a replacement record that shadows [from, to]
// (inclusive) and substitutes msg in derived history. The shadowed range
// must be within the log and fully contained in the current surface — the
// log validates this on commit (ErrSurfaceRange).
//
// NewReplaceEvent 构建替换记录：遮蔽 [from, to]（含端点）并以 msg 取代
// 派生历史中的该区间。遮蔽区间必须在日志内且完全包含于当前 surface——
// 日志在提交时校验（ErrSurfaceRange）。
func NewReplaceEvent(kind SessionEventKind, from, to int64, msg *Message) SessionEvent {
	return SessionEvent{
		Kind:       kind,
		Surface:    SessionSurfaceReplace,
		SourceFrom: from,
		SourceTo:   to,
		Message:    msg,
		At:         time.Now().UTC(),
	}
}

// NewLogOnlyEvent builds a bookkeeping record that never joins derived
// history.
//
// NewLogOnlyEvent 构建记账记录：绝不进入派生历史。
func NewLogOnlyEvent(kind SessionEventKind, meta map[string]any) SessionEvent {
	return SessionEvent{
		Kind:    kind,
		Surface: SessionSurfaceLogOnly,
		Meta:    meta,
		At:      time.Now().UTC(),
	}
}
