package session

import (
	"context"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

var _ SessionService = (*InMemorySessionService)(nil)

// InMemorySessionService persists sessions in memory (non-durable, for testing/light use).
// Sessions are stored in a map with user ID indexing for fast listing.
//
// InMemorySessionService 将会话存储在内存中（非持久，适用于测试或轻量场景）。
type InMemorySessionService struct {
	mu       sync.RWMutex
	sessions map[string]*sessionImpl
	userIdx  map[string][]string
}

// NewInMemorySessionService creates an in-memory session service.
// Data is lost on process restart.
//
// NewInMemorySessionService 创建内存会话服务（进程重启后数据丢失）。
func NewInMemorySessionService() *InMemorySessionService {
	return &InMemorySessionService{
		sessions: make(map[string]*sessionImpl),
		userIdx:  make(map[string][]string),
	}
}

// Create creates a new session in memory and indexes it by user ID.
// Create 在内存中创建新会话，并按用户 ID 建立索引。
func (s *InMemorySessionService) Create(_ context.Context, agentName, userID string, meta map[string]any) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	metaCopy := make(map[string]any, len(meta)+1)
	if meta != nil {
		for k, v := range meta {
			metaCopy[k] = v
		}
	}
	metaCopy["user_id"] = userID
	sess := &sessionImpl{
		id:        types.SessionID(),
		createdAt: time.Now(),
		agentName: agentName,
		meta:      metaCopy,
	}
	s.sessions[sess.id] = sess
	s.userIdx[userID] = append(s.userIdx[userID], sess.id)
	return sess, nil
}

// Get returns a deep copy of the session, or (nil, nil) when it does not exist.
// Get 返回会话的深拷贝，不存在时返回 (nil, nil)。
func (s *InMemorySessionService) Get(_ context.Context, sessionID string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil
	}
	return s.copySession(sess), nil
}

// Save stores the session. Unlike Get, Save does NOT deep-copy the session
// because the caller does not retain a reference after Save returns (P2).
//
// Save 存储会话。与 Get 不同，Save 不深拷贝会话，因为调用方在 Save 返回后
// 不再持有引用（P2）。
func (s *InMemorySessionService) Save(_ context.Context, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := session.(*sessionImpl)
	if !ok {
		return nil
	}
	s.sessions[sess.id] = sess
	return nil
}

// Delete removes the session from memory and from the user index.
// Delete 将会话从内存与用户索引中移除。
func (s *InMemorySessionService) Delete(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	for uid, ids := range s.userIdx {
		filtered := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != sessionID {
				filtered = append(filtered, id)
			}
		}
		s.userIdx[uid] = filtered
	}
	return nil
}

// List returns deep copies of all sessions belonging to the given user ID.
// List 返回属于指定用户 ID 的全部会话的深拷贝。
func (s *InMemorySessionService) List(_ context.Context, userID string) ([]Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.userIdx[userID]
	result := make([]Session, 0, len(ids))
	for _, id := range ids {
		if sess, ok := s.sessions[id]; ok {
			result = append(result, s.copySession(sess))
		}
	}
	return result, nil
}

func (s *InMemorySessionService) copySession(sess *sessionImpl) *sessionImpl {
	msgs := types.CloneMessages(sess.messages)
	meta := make(map[string]any, len(sess.meta))
	for k, v := range sess.meta {
		meta[k] = v
	}
	return &sessionImpl{
		id:         sess.id,
		messages:   msgs,
		usedTokens: sess.usedTokens,
		status:     sess.status,
		agentName:  sess.agentName,
		createdAt:  sess.createdAt,
		meta:       meta,
	}
}
