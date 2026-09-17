// Package session 的日志后端：LogSession 以 core/session.Log 为内核实现
// 现有 FROZEN Session 接口——消息历史是日志的派生投影（DSH 事件溯源
// 语义），持久化经 core/session.Store 完成。旧内存/文件后端保持不变；
// 本文件是"已验证的 dsh-session-persistence 语义"在 gocel 的落地。
package session

import (
	"context"
	"errors"
	"sync"
	"time"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

// errNotLogSession is returned by Save when the session is not a
// log-backed session.
var errNotLogSession = errors.New("session: not a log-backed session")

// logSession is a Session whose message history derives from an append-only
// event log. It implements the FROZEN Session contract as a thin shell over
// the log: AddMessage appends an event, GetMessages projects the surface,
// TrimMessages replaces the surface range with a log-only checkpoint —
// nothing is ever lost, the log is the single source of truth.
//
// logSession 是消息历史派生自只追加事件日志的 Session。它以薄壳实现
// FROZEN Session 契约：AddMessage 追加事件、GetMessages 投影 surface、
// TrimMessages 以 log-only 检查点替换 surface 区间——什么都不丢失，
// 日志是唯一事实源。
type logSession struct {
	id        string
	agentName string
	createdAt time.Time
	meta      map[string]any
	log       *coresession.Log
	mu        sync.RWMutex
}

// NewLogSession creates a session backed by a fresh event log. The log is
// the durable truth; message history is derived from it. The log and the
// session share ONE identity — projection registries key by log.ID and
// consumers key by session id, so the two must never diverge.
//
// NewLogSession 创建以全新事件日志为后端的会话。日志是持久真相；
// 消息历史从它派生。日志与会话共享同一身份——投影注册表按 log.ID 键控、
// 消费方按会话 id 键控，二者绝不分裂。
func NewLogSession(agentName string) *logSession {
	s := &logSession{
		id:        types.SessionID(),
		agentName: agentName,
		createdAt: time.Now(),
		meta:      make(map[string]any),
	}
	s.log = coresession.NewLog(s.id)
	return s
}

// Log exposes the underlying event log for replay, projection, and
// persistence (Store.Save of Log.Events).
//
// Log 暴露底层事件日志供重放、投影与持久化（Store.Save(Log.Events)）。
func (s *logSession) Log() *coresession.Log { return s.log }

// ID implements Session.
//
// ID 返回会话 ID（与底层日志共享同一身份）。
func (s *logSession) ID() string { return s.id }

// AgentName implements Session.
//
// AgentName 在读锁保护下返回会话的 Agent 名称。
func (s *logSession) AgentName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agentName
}

// CreatedAt implements Session.
//
// CreatedAt 返回会话的创建时间。
func (s *logSession) CreatedAt() time.Time { return s.createdAt }

// Meta folds the metadata map from the log's meta checkpoints (last-wins
// per key) — durable in the log, derived on read.
//
// Meta 从日志的 meta 检查点折叠出元数据映射（每键 last-wins）——持久于日志，
// 读取时派生。
func (s *logSession) Meta() map[string]any {
	out := make(map[string]any)
	for _, ev := range s.log.Events() {
		if ev.Kind != "session/meta" || ev.Meta == nil {
			continue
		}
		for k, v := range ev.Meta {
			out[k] = v
		}
	}
	return out
}

// UsedTokens folds the accumulated token usage from the log's log-only
// usage checkpoints (the log is the truth; the counter is derived).
//
// UsedTokens 从日志的 log-only 用量检查点折叠累计 token 用量（日志是真相；
// 计数是派生值）。
func (s *logSession) UsedTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.UsedTokensLocked()
}

// Status implements Session: folded from the meta checkpoints.
//
// Status 实现 Session：从 meta 检查点折叠得出状态。
func (s *logSession) Status() string {
	if v, ok := s.Meta()["status"].(string); ok {
		return v
	}
	return "active"
}

// AddMessages appends the messages to the event log, deriving each event
// kind from the message role (DSH surface vocabulary).
//
// AddMessages 将消息追加到事件日志，按消息角色派生各事件 kind（DSH surface
// 词汇）。
func (s *logSession) AddMessages(msgs []*types.Message) {
	for _, m := range msgs {
		s.AddMessage(m)
	}
}

// AddMessage appends one message to the event log as an append-surface
// event.
//
// AddMessage 将单条消息作为 append-surface 事件追加到事件日志。
func (s *logSession) AddMessage(msg *types.Message) {
	if msg == nil {
		return
	}
	var kind types.SessionEventKind
	switch msg.Role {
	case types.RoleSystem:
		kind = types.SessionEventSystem
	case types.RoleAssistant:
		kind = types.SessionEventAssistantMessage
	case types.RoleTool:
		kind = types.SessionEventToolResult
	default:
		kind = types.SessionEventUserMessage
	}
	_, _ = s.log.Append(types.NewSessionEvent(kind, msg))
}

// GetMessages derives the current message history from the log surface —
// a fresh, ordered projection (DSH deriveMessages).
//
// GetMessages 从日志 surface 派生当前消息历史——全新有序投影（DSH
// deriveMessages）。
func (s *logSession) GetMessages() []*types.Message {
	return s.log.DeriveMessages()
}

// AddTokenUsage appends a log-only usage checkpoint carrying the
// accumulated counter — durable in the log, derived on read. The fold and
// the append share one lock so concurrent callers cannot lose counts
// (read-modify-write is atomic).
//
// AddTokenUsage 追加携带累计计数的 log-only 用量检查点——持久于日志、读取时
// 派生。折叠与追加共享同一把锁，并发调用不会丢失计数（读-改-写原子）。
func (s *logSession) AddTokenUsage(usage int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.UsedTokensLocked()
	total := cur + usage
	_, _ = s.log.Append(types.NewLogOnlyEvent("session/usage", map[string]any{"used_tokens": total}))
}

// UsedTokensLocked folds the accumulated usage from the log; caller must
// hold s.mu.
//
// UsedTokensLocked 从日志折叠累计用量；调用方必须持有 s.mu。
func (s *logSession) UsedTokensLocked() int {
	total := 0
	for _, ev := range s.log.Events() {
		if ev.Kind == "session/usage" {
			if v, ok := ev.Meta["used_tokens"].(int); ok {
				total = v
			}
			if v, ok := ev.Meta["used_tokens"].(float64); ok {
				total = int(v)
			}
		}
	}
	return total
}

// SetAgentName implements Session.
//
// SetAgentName 在写锁保护下设置会话的 Agent 名称。
func (s *logSession) SetAgentName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentName = name
}

// SetStatus persists the status through the log's session/meta event —
// Status() folds it back, so the status survives save/load (DSH
// log-as-state).
//
// SetStatus 通过日志的 session/meta 事件持久化状态——Status() 折叠读回，
// 状态在保存/加载后仍存活（DSH log-as-state）。
func (s *logSession) SetStatus(status string) {
	s.SetMeta("status", status)
}

// TrimMessages replaces the whole derived surface with the given messages
// as a single replacement event — the shadowed history stays in the log
// (DSH compaction-style position replacement), and the surface becomes the
// trimmed view.
//
// TrimMessages 用给定消息以单个替换事件替换整个派生 surface——被遮蔽的历史
// 仍留在日志中（DSH 压缩式位置替换），surface 变为裁剪后的视图。
func (s *logSession) TrimMessages(msgs []*types.Message) {
	seqs := s.log.SurfaceSeq()
	if len(seqs) == 0 {
		s.AddMessages(msgs)
		return
	}
	// Replace [first, last] of the surface with one user message carrying
	// the trimmed content; log-only bookkeeping keeps the log compact.
	first, last := seqs[0], seqs[len(seqs)-1]
	if len(msgs) == 0 {
		_, _ = s.log.Append(types.NewReplaceEvent(types.SessionEventUserMessage, first, last, types.NewUserMessage("[history trimmed]")))
		return
	}
	// A single summary message substitutes the whole range.
	summary := types.NewUserMessage("[history trimmed to " + msgs[len(msgs)-1].Content + "]")
	_, _ = s.log.Append(types.NewReplaceEvent(types.SessionEventUserMessage, first, last, summary))
	// Append the retained messages after the replacement.
	for _, m := range msgs {
		s.AddMessage(m)
	}
}

// SetMeta appends a log-only meta checkpoint carrying the key — durable in
// the log, derived on read.
//
// SetMeta 追加携带键值的 log-only meta 检查点——持久于日志、读取时派生。
func (s *logSession) SetMeta(key string, value any) {
	if key == "" {
		return
	}
	_, _ = s.log.Append(types.NewLogOnlyEvent("session/meta", map[string]any{key: value}))
}

// logSessionService persists log-backed sessions through a
// core/session.Store — the durable backend is swappable (memory, file,
// database) exactly like dsh's session-persistence seam.
//
// logSessionService 经 core/session.Store 持久化日志后端会话——持久化
// 后端可整体替换（内存、文件、数据库），与 dsh 的 session-persistence
// 缝一致。
type logSessionService struct {
	store coresession.Store
}

// NewLogSessionService creates a log-backed session service over the given
// store.
//
// NewLogSessionService 在给定 store 上创建日志后端会话服务。
func NewLogSessionService(store coresession.Store) *logSessionService {
	return &logSessionService{store: store}
}

// Create implements SessionService: a fresh log-backed session.
//
// Create 实现 SessionService：创建全新的日志后端会话。
func (s *logSessionService) Create(_ context.Context, agentName, _ string, meta map[string]any) (Session, error) {
	ls := NewLogSession(agentName)
	for k, v := range meta {
		ls.SetMeta(k, v)
	}
	return ls, nil
}

// Get implements SessionService: rebuilds the session from the persisted
// event log (replay reconstruction — the log is the truth).
//
// Get 实现 SessionService：从持久化事件日志重建会话（重放重建——日志是真相）。
func (s *logSessionService) Get(ctx context.Context, sessionID string) (Session, error) {
	evs, err := s.store.Load(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	ls := &logSession{
		id:        sessionID,
		createdAt: time.Now(),
		meta:      make(map[string]any),
		log:       coresession.NewLog(sessionID),
	}
	if err := ls.log.Restore(evs); err != nil {
		return nil, err
	}
	return ls, nil
}

// Save implements SessionService: persists the complete event log
// atomically (whole-document replacement).
//
// Save 实现 SessionService：原子持久化完整事件日志（整文档替换）。
func (s *logSessionService) Save(ctx context.Context, session Session) error {
	ls, ok := session.(*logSession)
	if !ok {
		return errNotLogSession
	}
	return s.store.Save(ctx, ls.id, ls.log.Events())
}

// Delete implements SessionService.
//
// Delete 实现 SessionService：删除指定会话。
func (s *logSessionService) Delete(ctx context.Context, sessionID string) error {
	return s.store.Delete(ctx, sessionID)
}

// List implements SessionService.
//
// List 实现 SessionService：列出全部会话。
func (s *logSessionService) List(ctx context.Context, _ string) ([]Session, error) {
	ids, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(ids))
	for _, id := range ids {
		ls := &logSession{id: id, createdAt: time.Now(), meta: make(map[string]any), log: coresession.NewLog(id)}
		out = append(out, ls)
	}
	return out, nil
}
