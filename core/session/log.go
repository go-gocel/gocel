// Package session provides the session-log mechanism: an append-only,
// replay-safe event log whose LLM message history is a derived projection.
// It is the Go counterpart of DSH's event-sourced session with these
// semantics, kept deliberately minimal:
//
//   - The log is the single source of truth: every record carries a
//     monotonic contiguous Seq assigned on commit, and the log never
//     mutates or deletes committed records.
//   - Derived message history is a projection over the surface: append
//     records extend it; replacement records shadow an inclusive range and
//     substitute their message; log-only records never join it. Consumers
//     read DeriveMessages — a fresh, ordered slice.
//   - Consumers may subscribe to the change feed and receive each committed
//     record; the last committed Seq (Log.Seq) is the replay/subscription
//     watermark (types.RuntimeFacts.SessionLogSeq).
//   - Failures are honest: an out-of-range or out-of-surface replacement is
//     rejected (ErrSurfaceRange), never silently clipped; an event with a
//     pre-assigned Seq is rejected (ErrSeqAssigned).
//
// The package owns the mechanism only — when to append what (checkpoints,
// compaction policy, persistence backends, projections) belongs to gocel
// modules and products. It is safe for concurrent use.
//
// Package session 提供会话日志机制：只追加、可重放的事件日志，LLM 消息
// 历史是它的派生投影。它是 DSH 事件溯源会话的 Go 对应物，语义刻意保持
// 最小：
//
//   - 日志是唯一事实源：每条记录携带提交时分配的单调连续 Seq，已提交
//     记录绝不修改或删除。
//   - 派生消息历史是 surface 上的投影：append 记录扩展它；替换记录遮蔽
//     一个含端点区间并以自身消息取代；log-only 记录绝不进入。消费方读取
//     DeriveMessages——一份有序的新切片。
//   - 消费方可订阅变更馈送，逐条收到已提交记录；最近已提交 Seq
//     （Log.Seq）是回放/订阅水位（types.RuntimeFacts.SessionLogSeq）。
//   - 失败如实上报：区间越界或不在当前 surface 的替换被拒绝
//     （ErrSurfaceRange），绝不静默裁剪；预分配 Seq 的事件被拒绝
//     （ErrSeqAssigned）。
//
// 本包只拥有机制——何时追加什么（检查点、压缩策略、持久化后端、投影）
// 属于 gocel 模块与产品。并发安全。
package session

import (
	"errors"
	"fmt"
	"sync"

	"github.com/go-gocel/gocel/core/types"
)

// ErrSeqAssigned is returned when committing an event that already carries
// a Seq — sequence numbers are assigned by the log, never by callers.
//
// ErrSeqAssigned 在提交已携带 Seq 的事件时返回——序号由日志分配，
// 调用方不得预设。
var ErrSeqAssigned = errors.New("session: event already carries a seq")

// ErrSurfaceRange is returned when a replacement range is outside the log
// or not fully contained in the current surface.
//
// ErrSurfaceRange 在替换区间越界或未完全包含于当前 surface 时返回。
var ErrSurfaceRange = errors.New("session: replacement range not in surface")

// ErrClosed is returned when committing to a closed log.
//
// ErrClosed 在向已关闭日志提交时返回。
var ErrClosed = errors.New("session: log closed")

// surfaceNode is one live node of the derived-history projection.
type surfaceNode struct {
	seq  int64 // the log seq this node currently represents
	kind types.SessionEventKind
	msg  *types.Message
}

// Log is an append-only session event log. It is safe for concurrent use.
//
// Log 是只追加的会话事件日志。并发安全。
type Log struct {
	mu      sync.RWMutex
	id      string
	closed  bool
	seq     int64
	events  []types.SessionEvent
	surface []surfaceNode
	watches map[uint64]func(types.SessionEvent)
	nextID  uint64
}

// NewLog creates an empty log with the given identity.
// NewLog 以给定身份创建空日志。
func NewLog(id string) *Log {
	return &Log{
		id:      id,
		watches: make(map[uint64]func(types.SessionEvent)),
	}
}

// ID returns the log identity.
// ID 返回日志身份标识。
func (l *Log) ID() string { return l.id }

// Seq returns the last committed sequence number (0 for an empty log). It
// is the replay/subscription watermark.
//
// Seq 返回最近已提交序号（空日志为 0）。它是回放/订阅水位。
func (l *Log) Seq() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.seq
}

// Len returns the number of committed records.
// Len 返回已提交记录数。
func (l *Log) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.events)
}

// Append commits one event: assigns its Seq, appends it to the log, updates
// the derived surface, and notifies subscribers. The event's Seq must be 0
// (assigned here). Returns the committed copy.
//
// Append 提交一条事件：分配 Seq、追加进日志、更新派生 surface 并通知
// 订阅者。事件的 Seq 必须为 0（此处分配）。返回提交后的副本。
func (l *Log) Append(e types.SessionEvent) (types.SessionEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return types.SessionEvent{}, ErrClosed
	}
	if e.Seq != 0 {
		return types.SessionEvent{}, ErrSeqAssigned
	}
	l.seq++
	e.Seq = l.seq
	l.events = append(l.events, e)
	switch e.Surface {
	case types.SessionSurfaceAppend:
		l.surface = append(l.surface, surfaceNode{seq: e.Seq, kind: e.Kind, msg: e.Message})
	case types.SessionSurfaceReplace:
		// Shadow the inclusive range and substitute this record.
		if err := l.applyReplace(e); err != nil {
			// Roll back the log append: the surface must never diverge
			// from the log.
			l.seq--
			l.events = l.events[:len(l.events)-1]
			return types.SessionEvent{}, err
		}
	case types.SessionSurfaceLogOnly:
		// No surface change.
	}
	l.notifyLocked(e)
	return e, nil
}

// Restore rebuilds the log from a persisted, seq-contiguous event list
// (the load path of a durable backend). It commits the events verbatim —
// seqs are preserved, not reassigned — and reconstructs the surface by
// replaying append/replace semantics. Restore is only valid on an empty
// log; a non-empty or closed log fails loudly.
//
// Restore 从已持久化、seq 连续的事件列表重建日志（持久化后端的加载
// 路径）。事件按原样提交——seq 保留而非重新分配——并重放 append/replace
// 语义重建 surface。Restore 只对空日志有效；非空或已关闭的日志显式
// 报错。
func (l *Log) Restore(events []types.SessionEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	if len(l.events) != 0 || l.seq != 0 {
		return errors.New("session: restore only valid on an empty log")
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			return fmt.Errorf("session: restore requires contiguous seqs (event %d has seq %d)", i, e.Seq)
		}
	}
	// Validate the surface rebuild BEFORE committing: a failed restore must
	// leave the log untouched (fail-closed, no half-built state). The
	// validation replays append/replace against a scratch surface.
	var scratch []surfaceNode
	for _, e := range events {
		switch e.Surface {
		case types.SessionSurfaceAppend:
			scratch = append(scratch, surfaceNode{seq: e.Seq, kind: e.Kind, msg: e.Message})
		case types.SessionSurfaceReplace:
			replacement := surfaceNode{seq: e.Seq, kind: e.Kind, msg: e.Message}
			var err error
			scratch, err = applyReplaceScratch(scratch, e.SourceFrom, e.SourceTo, replacement)
			if err != nil {
				return fmt.Errorf("session: restore surface rebuild: %w", err)
			}
		case types.SessionSurfaceLogOnly:
			// No surface change.
		}
	}
	// Commit: seq, events, and the validated surface.
	l.seq = int64(len(events))
	l.events = append(l.events, events...)
	l.surface = scratch
	return nil
}

// applyReplaceScratch shadows [from, to] (inclusive) in the scratch surface
// and substitutes the replacement node at the same position. It is the
// pure validation counterpart of applyReplace, operating on a copy so a
// failed restore leaves the log untouched.
func applyReplaceScratch(surface []surfaceNode, from, to int64, replacement surfaceNode) ([]surfaceNode, error) {
	if from <= 0 || to < from {
		return nil, fmt.Errorf("%w: [%d, %d]", ErrSurfaceRange, from, to)
	}
	startIdx := -1
	for i, n := range surface {
		if n.seq == from {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return nil, fmt.Errorf("%w: seq %d not on surface", ErrSurfaceRange, from)
	}
	want := int(to - from + 1)
	shadowed := 0
	for i := startIdx; i < len(surface); i++ {
		seq := surface[i].seq
		if seq >= from && seq <= to {
			shadowed++
			continue
		}
		break
	}
	if shadowed != want {
		return nil, fmt.Errorf("%w: range [%d, %d] not fully on surface (found %d of %d)",
			ErrSurfaceRange, from, to, shadowed, want)
	}
	kept := make([]surfaceNode, 0, len(surface)-shadowed+1)
	kept = append(kept, surface[:startIdx]...)
	kept = append(kept, replacement)
	kept = append(kept, surface[startIdx+shadowed:]...)
	return kept, nil
}

// applyReplace shadows [SourceFrom, SourceTo] (inclusive) in the surface
// and substitutes the event's message at the same position. Called with
// l.mu held. The range must be fully contained in the current surface —
// a partial or absent range is rejected (ErrSurfaceRange), never clipped.
// After a replacement the surface order no longer tracks seq order: the
// replacement node keeps the position of the range it shadowed.
func (l *Log) applyReplace(e types.SessionEvent) error {
	replacement := surfaceNode{seq: e.Seq, kind: e.Kind, msg: e.Message}
	next, err := applyReplaceScratch(l.surface, e.SourceFrom, e.SourceTo, replacement)
	if err != nil {
		return err
	}
	l.surface = next
	return nil
}

// Events returns a copy of the complete committed log, in seq order.
// Replacement records are included — the log never drops shadowed history.
//
// Events 返回完整已提交日志的副本，按 seq 排序。替换记录包含在内——
// 日志绝不丢弃被遮蔽的历史。
func (l *Log) Events() []types.SessionEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]types.SessionEvent, len(l.events))
	copy(out, l.events)
	return out
}

// DeriveMessages returns a fresh, ordered slice of the derived message
// history — the LLM-visible conversation. Append records contribute their
// message in order; replacement records substitute the shadowed range;
// log-only records never appear.
//
// DeriveMessages 返回派生消息历史的一份新切片——模型可见的对话。
// append 记录按序贡献消息；替换记录取代被遮蔽区间；log-only 记录不出现。
func (l *Log) DeriveMessages() []*types.Message {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*types.Message, 0, len(l.surface))
	for _, n := range l.surface {
		if n.msg != nil {
			out = append(out, n.msg)
		}
	}
	return out
}

// SurfaceSeq returns the seqs currently on the derived surface, in order.
// Consumers use it to verify replacement ranges against the live surface.
//
// SurfaceSeq 返回当前派生 surface 上的 seq 序列（按序）。消费方用它校验
// 替换区间是否落在当前 surface 上。
func (l *Log) SurfaceSeq() []int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]int64, 0, len(l.surface))
	for _, n := range l.surface {
		out = append(out, n.seq)
	}
	return out
}

// Subscribe registers a change listener that receives every committed
// record (append and replace) after the subscription is established. The
// callback runs synchronously inside the commit path — keep it light and
// never call back into the log. Returns an idempotent cancel function.
//
// Subscribe 注册变更监听器：订阅建立后收到每一条已提交记录（append 与
// replace）。回调在提交路径内同步执行——保持轻量，切勿回调日志本身。
// 返回幂等 cancel。
func (l *Log) Subscribe(fn func(types.SessionEvent)) (cancel func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextID++
	id := l.nextID
	l.watches[id] = fn
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.watches, id)
			l.mu.Unlock()
		})
	}
}

// Close seals the log: further appends fail with ErrClosed. The derived
// surface and committed records remain readable.
//
// Close 封存日志：后续追加以 ErrClosed 失败。派生 surface 与已提交记录
// 仍可读。
func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
}

// notifyLocked delivers one committed record to subscribers. Called with
// l.mu held; subscribers are invoked in registration order.
func (l *Log) notifyLocked(e types.SessionEvent) {
	for _, fn := range l.watches {
		fn(e)
	}
}
