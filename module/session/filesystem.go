package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

var _ SessionService = (*FileSystemSessionService)(nil)

type sessionFile struct {
	ID         string         `json:"id"`
	AgentName  string         `json:"agent_name"`
	Status     string         `json:"status"`
	CreatedAt  time.Time      `json:"created_at"`
	Meta       map[string]any `json:"meta"`
	UsedTokens int            `json:"used_tokens"`
	Messages   []*messageFile `json:"messages,omitempty"`
	UserID     string         `json:"user_id"`
}

type messageFile struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []types.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

func sessionToFile(s *sessionImpl, userID string) *sessionFile {
	sf := &sessionFile{
		ID:         s.id,
		AgentName:  s.agentName,
		Status:     s.status,
		CreatedAt:  s.CreatedAt(),
		Meta:       s.Meta(),
		UsedTokens: s.UsedTokens(),
		UserID:     userID,
	}
	if uid, ok := s.Meta()["user_id"]; ok {
		sf.UserID, _ = uid.(string)
	}
	for _, m := range s.GetMessages() {
		sf.Messages = append(sf.Messages, &messageFile{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		})
	}
	return sf
}

func fileToSession(sf *sessionFile) *sessionImpl {
	s := &sessionImpl{
		id:         sf.ID,
		agentName:  sf.AgentName,
		status:     sf.Status,
		createdAt:  sf.CreatedAt,
		meta:       sf.Meta,
		usedTokens: sf.UsedTokens,
	}
	if s.meta == nil {
		s.meta = make(map[string]any)
	}
	s.meta["user_id"] = sf.UserID
	for _, mf := range sf.Messages {
		s.messages = append(s.messages, &types.Message{
			Role:       types.Role(mf.Role),
			Content:    mf.Content,
			ToolCalls:  mf.ToolCalls,
			ToolCallID: mf.ToolCallID,
		})
	}
	return s
}

// FileSystemSessionService persists sessions as JSON files on disk.
// Sessions are indexed by user ID for fast lookup.
// A simple in-memory index cache avoids reading _index.json on every operation (P1).
//
// FileSystemSessionService 将会话持久化为磁盘 JSON 文件，按用户 ID 索引。
type FileSystemSessionService struct {
	dir          string
	mu           sync.Mutex
	userIdx      map[string][]string
	indexLoaded  bool
	sessionCache map[string]*sessionImpl // p1: in-memory cache of recently accessed sessions
}

// NewFileSystemSessionService creates a file-system-backed session service.
// dir: directory for session JSON files (created if missing).
//
// NewFileSystemSessionService 创建基于文件系统的会话持久化服务。
func NewFileSystemSessionService(dir string) *FileSystemSessionService {
	os.MkdirAll(dir, 0755)
	return &FileSystemSessionService{
		dir:          dir,
		userIdx:      make(map[string][]string),
		sessionCache: make(map[string]*sessionImpl),
	}
}

func (s *FileSystemSessionService) sessionPath(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// validateSessionID rejects caller-controlled session ids that could
// escape the store directory when joined into a path (Get/Save/Delete
// accept arbitrary ids — a traversal id reads/writes/deletes files
// outside the store, a verified critical defect).
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session: empty id")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return fmt.Errorf("session: invalid id %q", id)
	}
	return nil
}

func (s *FileSystemSessionService) indexPath() string {
	return filepath.Join(s.dir, "_index.json")
}

type indexFile struct {
	UserIdx map[string][]string `json:"user_idx"`
}

func (s *FileSystemSessionService) ensureIndexLoaded() {
	if s.indexLoaded {
		return
	}
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		s.indexLoaded = true
		return
	}
	var idx indexFile
	if json.Unmarshal(data, &idx) == nil {
		s.userIdx = idx.UserIdx
	}
	if s.userIdx == nil {
		s.userIdx = make(map[string][]string)
	}
	s.indexLoaded = true
}

func (s *FileSystemSessionService) saveIndex() {
	idx := &indexFile{UserIdx: s.userIdx}
	data, _ := json.MarshalIndent(idx, "", "  ")
	os.WriteFile(s.indexPath(), data, 0644)
}

// Create creates a new session, persists it as a JSON file, and indexes it
// by user ID.
// Create 创建新会话，将其持久化为 JSON 文件，并按用户 ID 建立索引。
func (s *FileSystemSessionService) Create(_ context.Context, agentName, userID string, meta map[string]any) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureIndexLoaded()

	metaCopy := make(map[string]any, len(meta)+1)
	for k, v := range meta {
		metaCopy[k] = v
	}
	metaCopy["user_id"] = userID

	sess := &sessionImpl{
		id:        types.SessionID(),
		status:    "active",
		createdAt: time.Now(),
		agentName: agentName,
		meta:      metaCopy,
	}

	sf := sessionToFile(sess, userID)
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal session: %w", err)
	}
	if err := os.WriteFile(s.sessionPath(sess.id), data, 0644); err != nil {
		return nil, fmt.Errorf("write session file: %w", err)
	}

	s.userIdx[userID] = append(s.userIdx[userID], sess.id)
	s.saveIndex()
	s.sessionCache[sess.id] = sess

	return sess, nil
}

// Get loads a session by ID, returning (nil, nil) when it does not exist.
// Get 按 ID 加载会话，不存在时返回 (nil, nil)。
func (s *FileSystemSessionService) Get(_ context.Context, sessionID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}

	// Check cache first (P1).
	if cached, ok := s.sessionCache[sessionID]; ok {
		return cached, nil
	}

	data, err := os.ReadFile(s.sessionPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session %q: %w", sessionID, err)
	}

	var sf sessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("unmarshal session %q: %w", sessionID, err)
	}

	sess := fileToSession(&sf)
	s.sessionCache[sessionID] = sess // populate cache
	return sess, nil
}

// Save persists the session's current state to its JSON file.
// Save 把会话当前状态持久化到其 JSON 文件。
func (s *FileSystemSessionService) Save(_ context.Context, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureIndexLoaded()

	sess, ok := session.(*sessionImpl)
	if !ok {
		return fmt.Errorf("expected *sessionImpl, got %T", session)
	}
	if err := validateSessionID(sess.id); err != nil {
		return err
	}

	userID, _ := sess.Meta()["user_id"].(string)

	sf := sessionToFile(sess, userID)
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	if err := os.WriteFile(s.sessionPath(sess.id), data, 0644); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}

	// Update cache.
	s.sessionCache[sess.id] = sess

	// Update index if needed (P1: skip full index scan if session already tracked).
	found := false
	for _, id := range s.userIdx[userID] {
		if id == sess.id {
			found = true
			break
		}
	}
	if !found {
		s.userIdx[userID] = append(s.userIdx[userID], sess.id)
		s.saveIndex()
	}

	return nil
}

// Delete removes the session's file and drops it from the cache and index.
// Delete 删除会话文件，并将其从缓存与索引中移除。
func (s *FileSystemSessionService) Delete(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureIndexLoaded()

	if err := validateSessionID(sessionID); err != nil {
		return err
	}

	if err := os.Remove(s.sessionPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete session %q: %w", sessionID, err)
	}

	// Remove from cache.
	delete(s.sessionCache, sessionID)

	for uid, ids := range s.userIdx {
		filtered := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != sessionID {
				filtered = append(filtered, id)
			}
		}
		s.userIdx[uid] = filtered
	}
	s.saveIndex()

	return nil
}

// List returns all sessions belonging to the given user ID.
// List 返回属于指定用户 ID 的全部会话。
func (s *FileSystemSessionService) List(_ context.Context, userID string) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureIndexLoaded()

	ids := s.userIdx[userID]
	result := make([]Session, 0, len(ids))
	for _, id := range ids {
		// Check cache first.
		if cached, ok := s.sessionCache[id]; ok {
			result = append(result, cached)
			continue
		}
		data, err := os.ReadFile(s.sessionPath(id))
		if err != nil {
			continue
		}
		var sf sessionFile
		if json.Unmarshal(data, &sf) != nil {
			continue
		}
		sess := fileToSession(&sf)
		s.sessionCache[id] = sess
		result = append(result, sess)
	}
	return result, nil
}
