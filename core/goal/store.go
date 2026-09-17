package goal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// randomSuffix returns 8 random bytes hex-encoded, for id uniqueness.
func randomSuffix() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// MemoryStore is an in-process Store for development and tests. It is safe
// for concurrent use and returns copies.
// MemoryStore 是面向开发与测试的进程内 Store。并发安全且返回副本。
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]*Goal
}

// NewMemoryStore creates an empty memory store.
// NewMemoryStore 创建空的内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]*Goal)}
}

func copyGoal(g *Goal) *Goal {
	if g == nil {
		return nil
	}
	c := *g
	return &c
}

// Save persists the goal, replacing any existing document for the same id.
// Save 持久化目标，替换同 id 的既有文档。
func (s *MemoryStore) Save(_ context.Context, g *Goal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[g.ID] = copyGoal(g)
	return nil
}

// Load returns a copy of the goal, or ErrNotFound.
// Load 返回目标的副本；不存在时返回 ErrNotFound。
func (s *MemoryStore) Load(_ context.Context, id string) (*Goal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.items[id]
	if !ok {
		return nil, ErrNotFound
	}
	return copyGoal(g), nil
}

// List returns every goal ordered by creation time.
// List 按创建时间返回全部目标。
func (s *MemoryStore) List(_ context.Context) ([]*Goal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Goal, 0, len(s.items))
	for _, g := range s.items {
		out = append(out, copyGoal(g))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Delete removes the goal; deleting a missing id is a no-op.
// Delete 删除目标；删除不存在的 id 是无操作。
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return nil
}

// FileStore persists one JSON document per goal under a directory. Writes
// are atomic (temp file + rename); reads are strict — a corrupt document is
// an error, never a silent default (harness-owned data parses strictly).
//
// FileStore 在一个目录下每个目标持久化一个 JSON 文档。写入原子
// （临时文件 + rename）；读取严格——损坏的文档报错，绝不静默默认
// （harness 自有数据严格解析）。
type FileStore struct {
	dir string
}

// NewFileStore creates the store, creating the directory if needed.
// NewFileStore 创建存储，必要时创建目录。
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("goal: create store dir: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

func (s *FileStore) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Save persists the goal as a JSON document via atomic temp-file + rename.
// Save 以 JSON 文档持久化目标，通过临时文件 + rename 原子写入。
func (s *FileStore) Save(_ context.Context, g *Goal) error {
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("goal: marshal %s: %w", g.ID, err)
	}
	tmp := s.path(g.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("goal: write %s: %w", g.ID, err)
	}
	if err := os.Rename(tmp, s.path(g.ID)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("goal: publish %s: %w", g.ID, err)
	}
	return nil
}

// Load reads the goal's JSON document; a missing id returns ErrNotFound,
// a corrupt document returns an error.
// Load 读取目标的 JSON 文档；缺失返回 ErrNotFound，损坏返回错误。
func (s *FileStore) Load(_ context.Context, id string) (*Goal, error) {
	data, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("goal: read %s: %w", id, err)
	}
	var g Goal
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("goal: corrupt document %s: %w", id, err)
	}
	return &g, nil
}

// List returns every goal in the directory ordered by creation time. A
// corrupt document fails the listing (strict reads).
// List 按创建时间返回目录中的全部目标；损坏的文档使列举失败（严格读取）。
func (s *FileStore) List(_ context.Context) ([]*Goal, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("goal: list dir: %w", err)
	}
	var out []*Goal
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || e.Name() == "" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		g, err := s.Load(context.Background(), id)
		if err != nil {
			return nil, err // strict: a corrupt document fails the listing
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Delete removes the goal's document; deleting a missing id is a no-op.
// Delete 删除目标文档；删除不存在的 id 是无操作。
func (s *FileStore) Delete(_ context.Context, id string) error {
	err := os.Remove(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Ensure Store implementations stay honest: the CAS contract depends on
// whole-document replacement.
var (
	_ Store = (*MemoryStore)(nil)
	_ Store = (*FileStore)(nil)
)
