// Package session provides Session persistence and the SessionObserver.
//
// Session stores conversation history, token usage, and status for each agent run.
// SessionObserver (a runner.Module) automatically persists run results via
// HookAfterAgentRun when the Runner completes. Supports in-memory and filesystem backends.
//
// 会话持久化包。Session 存储每次 Agent 运行的对话历史、Token 用量和状态。
// SessionObserver（runner.Module 实现，通过 HookAfterAgentRun 钩子）在 Runner 完成后自动持久化结果。
// 支持内存和文件系统两种后端。
package session

import (
	"context"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

// Session manages a conversation session with messages, tokens, and metadata.
//
// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// Session 管理一个会话，包含消息、Token 用量和元数据。
type Session interface {
	ID() string
	AgentName() string
	CreatedAt() time.Time
	Meta() map[string]any
	UsedTokens() int
	Status() string

	AddMessages(msgs []*types.Message)
	AddMessage(msg *types.Message)
	GetMessages() []*types.Message
	AddTokenUsage(usage int)
	SetAgentName(name string)
	SetStatus(status string)
	TrimMessages(msgs []*types.Message)
}

// SessionService manages conversation persistence (create, read, save, delete, list).
//
// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// SessionService 管理会话持久化操作（创建、读取、保存、删除、列表）。
type SessionService interface {
	Create(ctx context.Context, agentName, userID string, meta map[string]any) (Session, error)
	Get(ctx context.Context, sessionID string) (Session, error)
	Save(ctx context.Context, session Session) error
	Delete(ctx context.Context, sessionID string) error
	List(ctx context.Context, userID string) ([]Session, error)
}

var _ Session = (*sessionImpl)(nil)

// sessionImpl implements Session with in-memory storage of messages, token usage, and metadata.
// Provides concurrency-safe access with RWMutex.
//
// sessionImpl 实现 Session 接口，内存存储消息、Token 用量和元数据，并发安全。
type sessionImpl struct {
	id         string
	messages   []*types.Message
	usedTokens int
	agentName  string
	status     string
	createdAt  time.Time
	meta       map[string]any
	mu         sync.RWMutex
}

// NewSession creates a new Session with a generated ID and "active" status.
//
// NewSession 创建新会话，自动生成 ID，状态为 "active"。
func NewSession(agentName string) *sessionImpl {
	return &sessionImpl{
		id:        types.SessionID(),
		status:    "active",
		createdAt: time.Now(),
		agentName: agentName,
		meta:      make(map[string]any),
	}
}

// ID returns the session ID.
// ID 返回会话 ID。
func (s *sessionImpl) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}
// AgentName returns the agent name.
// AgentName 返回会话的 Agent 名称。
func (s *sessionImpl) AgentName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agentName
}
// Status returns the session status.
// Status 返回会话状态。
func (s *sessionImpl) Status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}
// CreatedAt returns the session creation time.
// CreatedAt 返回会话创建时间。
func (s *sessionImpl) CreatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.createdAt
}
// Meta returns a clone of the session metadata map.
// Meta 返回会话元数据映射的副本。
func (s *sessionImpl) Meta() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clone := make(map[string]any, len(s.meta))
	for k, v := range s.meta {
		clone[k] = v
	}
	return clone
}
// UsedTokens returns the accumulated token usage.
// UsedTokens 返回累计的 token 用量。
func (s *sessionImpl) UsedTokens() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usedTokens
}
// SetStatus sets the session status.
// SetStatus 设置会话状态。
func (s *sessionImpl) SetStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// AddMessages appends the messages to the session.
// AddMessages 将消息追加到会话。
func (s *sessionImpl) AddMessages(msgs []*types.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msgs...)
}

// AddMessage appends one message to the session.
// AddMessage 将单条消息追加到会话。
func (s *sessionImpl) AddMessage(msg *types.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
}

// GetMessages returns a clone of the session messages.
// GetMessages 返回会话消息的克隆。
func (s *sessionImpl) GetMessages() []*types.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return types.CloneMessages(s.messages)
}

// AddTokenUsage adds the usage to the accumulated total.
// AddTokenUsage 将用量累加到累计总数。
func (s *sessionImpl) AddTokenUsage(usage int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usedTokens += usage
}

// SetAgentName sets the agent name.
// SetAgentName 设置 Agent 名称。
func (s *sessionImpl) SetAgentName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentName = name
}

// TrimMessages replaces the session messages with a clone of the given ones.
// TrimMessages 用给定消息的克隆替换会话消息。
func (s *sessionImpl) TrimMessages(msgs []*types.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = types.CloneMessages(msgs)
}

// SetMeta sets a metadata key on the session.
// SetMeta 设置会话的元数据键。
func (s *sessionImpl) SetMeta(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta[key] = value
}
