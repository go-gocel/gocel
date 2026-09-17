package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

// ArchivedSession represents a single cross-session memory record.
//
// ArchivedSession 表示一条跨会话记忆记录。
type ArchivedSession struct {
	SessionID        string    `json:"session_id"`
	Summary          string    `json:"summary"`
	CreatedAt        time.Time `json:"created_at"`
	InteractionCount int       `json:"interaction_count"`
	KeyTopics        []string  `json:"key_topics,omitempty"`

	// Decay/forgetting fields
	Importance     float64   `json:"importance"`       // current importance (1.0 = default)
	AccessCount    int       `json:"access_count"`     // times accessed since last decay sweep
	LastAccessedAt time.Time `json:"last_accessed_at"` // last access time
}

// ArchiveMemory provides cross-session long-term memory via JSON file persistence.
// It stores condensed session-level summaries that survive agent restarts.
//
// ArchiveMemory 通过 JSON 文件持久化提供跨会话长期记忆，保存压缩后的会话级
// 摘要，可跨代理重启存活。
type ArchiveMemory struct {
	mu          sync.RWMutex
	sessions    []ArchivedSession
	filePath    string
	maxSessions int
	ready       bool
}

// NewArchiveMemory creates an ArchiveMemory backed by the given file path.
//
// NewArchiveMemory 创建由给定文件路径支撑的 ArchiveMemory。
func NewArchiveMemory(filePath string, maxSessions int) *ArchiveMemory {
	if maxSessions <= 0 {
		maxSessions = 5
	}
	return &ArchiveMemory{
		filePath:    filePath,
		maxSessions: maxSessions,
	}
}

// Load reads archived sessions from the JSON file. If the file doesn't exist,
// it silently returns nil.
//
// Load 从 JSON 文件读取已归档会话；文件不存在时静默返回 nil。
func (a *ArchiveMemory) Load() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.filePath == "" {
		a.ready = true
		return nil
	}

	data, err := os.ReadFile(a.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			a.sessions = nil
			a.ready = true
			return nil
		}
		return fmt.Errorf("archive load: %w", err)
	}

	var sessions []ArchivedSession
	if err := json.Unmarshal(data, &sessions); err != nil {
		return fmt.Errorf("archive decode: %w", err)
	}
	a.sessions = sessions
	a.ready = true
	return nil
}

// Save persists all archived sessions to the JSON file.
//
// Save 将所有归档会话持久化到 JSON 文件。
func (a *ArchiveMemory) Save() error {
	a.mu.RLock()
	sessions := a.sessions
	a.mu.RUnlock()

	if a.filePath == "" || len(sessions) == 0 {
		return nil
	}

	// Ensure directory exists
	dir := filepath.Dir(a.filePath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("archive mkdir: %w", err)
		}
	}

	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return fmt.Errorf("archive encode: %w", err)
	}

	if err := os.WriteFile(a.filePath, data, 0644); err != nil {
		return fmt.Errorf("archive write: %w", err)
	}
	return nil
}

// AddSession appends a new archived session record. If the count exceeds
// maxSessions, the oldest entries are pruned.
//
// AddSession 追加一条归档会话记录；数量超过 maxSessions 时裁剪最旧条目。
func (a *ArchiveMemory) AddSession(summary string, count int, topics []string) {
	if summary == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if topics == nil {
		topics = []string{}
	}

	session := ArchivedSession{
		SessionID:        types.SessionID(),
		Summary:          summary,
		CreatedAt:        time.Now(),
		InteractionCount: count,
		KeyTopics:        topics,
		Importance:       1.0,
		LastAccessedAt:   time.Now(),
	}

	a.sessions = append(a.sessions, session)

	// Prune oldest if over limit
	if len(a.sessions) > a.maxSessions {
		excess := len(a.sessions) - a.maxSessions
		a.sessions = a.sessions[excess:]
	}
}

// GetRecentSessions returns the most recent N archived sessions (oldest first).
// It also increments AccessCount and updates LastAccessedAt for tracking.
//
// GetRecentSessions 返回最近的 N 条归档会话（旧在前）；同时递增 AccessCount
// 并更新 LastAccessedAt 以用于跟踪。
func (a *ArchiveMemory) GetRecentSessions(n int) []ArchivedSession {
	a.mu.Lock()
	defer a.mu.Unlock()

	if n <= 0 {
		return nil
	}
	if n > len(a.sessions) {
		n = len(a.sessions)
	}

	// Update access tracking for the returned sessions
	now := time.Now()
	for i := len(a.sessions) - n; i < len(a.sessions); i++ {
		a.sessions[i].AccessCount++
		a.sessions[i].LastAccessedAt = now
	}

	result := make([]ArchivedSession, n)
	copy(result, a.sessions[len(a.sessions)-n:])
	return result
}

// BuildContext formats recent archived sessions into a string for the system prompt.
// maxChars limits the total output length; maxSessions limits how many sessions to include.
//
// BuildContext 把最近的归档会话格式化为系统提示词字符串；maxChars 限制总输出
// 长度，maxSessions 限制包含的会话数量。
func (a *ArchiveMemory) BuildContext(maxSessions, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}

	sessions := a.GetRecentSessions(maxSessions)
	if len(sessions) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[Cross-session memory]\n")

	for i, s := range sessions {
		if s.Summary == "" {
			continue
		}
		line := fmt.Sprintf("  [Session %d] %s (turns: %d)\n", i+1, s.Summary, s.InteractionCount)
		if sb.Len()+len(line) > maxChars {
			break
		}
		sb.WriteString(line)
	}

	return sb.String()
}

// Clear removes all archived sessions (does NOT persist to file — call Save() separately).
//
// Clear 移除所有归档会话（不落盘——请另行调用 Save()）。
func (a *ArchiveMemory) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions = nil
}

// Decay applies the MemoryDecay sweep to all archived sessions, forgetting
// those whose importance drops below the minimum threshold.
// It returns the number of forgotten (removed) sessions.
// This method acquires the write lock and modifies sessions in-place.
//
// Decay 对所有归档会话执行 MemoryDecay 清扫，遗忘重要性低于阈值的会话，
// 返回被遗忘（移除）的会话数。本方法持有写锁并就地修改会话。
func (a *ArchiveMemory) Decay(d *MemoryDecay) int {
	if d == nil {
		return 0
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.ready {
		return 0
	}

	before := len(a.sessions)
	a.sessions = d.Sweep(a.sessions, time.Now())
	return before - len(a.sessions)
}

// Sessions returns a copy of all archived sessions (for inspection/testing).
//
// Sessions 返回所有归档会话的副本（供检查/测试）。
func (a *ArchiveMemory) Sessions() []ArchivedSession {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := make([]ArchivedSession, len(a.sessions))
	copy(result, a.sessions)
	return result
}

// Len returns the number of archived sessions.
//
// Len 返回归档会话的数量。
func (a *ArchiveMemory) Len() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.sessions)
}

// Ready returns true if Load() has been called.
//
// Ready 在 Load() 已被调用时返回 true。
func (a *ArchiveMemory) Ready() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ready
}

// extractKeyTopics attempts to extract key topics from an interaction summary.
// This is a simple heuristic: picks the first few content words.
func extractKeyTopics(summary string, maxTopics int) []string {
	if summary == "" || maxTopics <= 0 {
		return nil
	}

	// Split by common delimiters and take the first maxTopics non-empty phrases
	parts := strings.FieldsFunc(summary, func(r rune) bool {
		return r == ',' || r == ';'
	})

	var topics []string
	seen := make(map[string]bool)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		// Only take reasonably short phrases (3-60 chars)
		runes := []rune(p)
		if len(runes) >= 3 && len(runes) <= 60 {
			topics = append(topics, p)
			seen[p] = true
			if len(topics) >= maxTopics {
				break
			}
		}
	}
	return topics
}
