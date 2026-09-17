package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

var _ kernel.CheckpointStore = (*InMemoryCheckpointStore)(nil)
var _ kernel.CheckpointStore = (*FileSystemCheckpointStore)(nil)

// Option configures a checkpoint store.
//
// Option 配置 checkpoint 存储。
type Option func(*options)

type options struct {
	maxCheckpoints int // 0 = unlimited
}

// WithMaxCheckpoints limits the number of stored checkpoints.
// When the limit is reached, the oldest checkpoint (by insertion order)
// is evicted on each Save. 0 or negative means unlimited.
//
// WithMaxCheckpoints 限制存储的 checkpoint 数量上限。
// 达到上限后，每次 Save 淘汰最早插入的 checkpoint。0 或负数表示不限制。
func WithMaxCheckpoints(n int) Option {
	return func(o *options) { o.maxCheckpoints = n }
}

// InMemoryCheckpointStore is an in-memory implementation of CheckpointStore.
//
// InMemoryCheckpointStore 是 CheckpointStore 的内存实现。
type InMemoryCheckpointStore struct {
	mu    sync.RWMutex
	cps   map[string]*types.Checkpoint
	dirs  []string
	order []string // insertion order, for max-capacity eviction
	max   int
}

// NewInMemoryCheckpointStore creates an in-memory checkpoint store (non-persistent).
//
// NewInMemoryCheckpointStore 创建内存版 checkpoint 存储（不持久化）。
func NewInMemoryCheckpointStore(opts ...Option) *InMemoryCheckpointStore {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return &InMemoryCheckpointStore{
		cps: make(map[string]*types.Checkpoint),
		max: o.maxCheckpoints,
	}
}

func cloneCheckpoint(cp *types.Checkpoint) *types.Checkpoint {
	if cp == nil {
		return nil
	}
	return &types.Checkpoint{
		ID:              cp.ID,
		AgentName:       cp.AgentName,
		SessionID:       cp.SessionID,
		Messages:        types.CloneMessages(cp.Messages),
		SystemPrompt:    cp.SystemPrompt,
		EnableStreaming: cp.EnableStreaming,
		MaxSteps:        cp.MaxSteps,
		StepIndex:       cp.StepIndex,
		CreatedAt:       cp.CreatedAt,
	}
}

// Save stores a cloned checkpoint, evicting the oldest beyond the capacity
// limit.
//
// Save 存储 checkpoint 的副本，超过容量上限时淘汰最早的。
func (s *InMemoryCheckpointStore) Save(_ context.Context, cp *types.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cps[cp.ID]; !exists {
		s.order = append(s.order, cp.ID)
	}
	s.cps[cp.ID] = cloneCheckpoint(cp)
	if cp.SessionID != "" {
		exists := false
		for _, d := range s.dirs {
			if d == cp.SessionID {
				exists = true
				break
			}
		}
		if !exists {
			s.dirs = append(s.dirs, cp.SessionID)
		}
	}
	s.evictLocked()
	return nil
}

// evictLocked removes the oldest checkpoints beyond the capacity limit.
// Caller must hold s.mu.
func (s *InMemoryCheckpointStore) evictLocked() {
	if s.max <= 0 {
		return
	}
	for len(s.order) > s.max {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.cps, oldest)
	}
}

// Load returns a clone of the checkpoint with the given id, or an error
// when it does not exist.
//
// Load 返回指定 id 的 checkpoint 副本，不存在时返回错误。
func (s *InMemoryCheckpointStore) Load(_ context.Context, id string) (*types.Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp, ok := s.cps[id]
	if !ok {
		return nil, fmt.Errorf("checkpoint %q not found", id)
	}
	return cloneCheckpoint(cp), nil
}

// Delete removes the checkpoint with the given id.
//
// Delete 删除指定 id 的 checkpoint。
func (s *InMemoryCheckpointStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cps, id)
	return nil
}

// List returns the sorted ids of all stored checkpoints.
//
// List 返回所有已存 checkpoint 的排序后的 id 列表。
func (s *InMemoryCheckpointStore) List(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.cps))
	for id := range s.cps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// FileSystemCheckpointStore persists checkpoints to the filesystem.
//
// FileSystemCheckpointStore 把 checkpoint 持久化到文件系统。
type FileSystemCheckpointStore struct {
	dir   string
	mu    sync.RWMutex
	order []string // insertion order, for max-capacity eviction
	max   int
}

// NewFileSystemCheckpointStore creates a filesystem-backed checkpoint store.
//
// NewFileSystemCheckpointStore 创建基于文件系统的 checkpoint 存储。
func NewFileSystemCheckpointStore(dir string, opts ...Option) (*FileSystemCheckpointStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create checkpoint dir %q: %w", dir, err)
	}
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return &FileSystemCheckpointStore{dir: dir, max: o.maxCheckpoints}, nil
}

// Save writes the checkpoint as a JSON file, evicting the oldest files
// beyond the capacity limit.
//
// Save 将 checkpoint 以 JSON 文件写入，超过容量上限时淘汰最旧的文件。
func (s *FileSystemCheckpointStore) Save(_ context.Context, cp *types.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	path := filepath.Join(s.dir, cp.ID+".ckpt")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if _, exists := s.orderSetLocked(cp.ID); !exists {
		s.order = append(s.order, cp.ID)
	}
	s.evictLocked()
	return nil
}

// orderSetLocked reports whether id is already tracked in order.
// Caller must hold s.mu.
func (s *FileSystemCheckpointStore) orderSetLocked(id string) (string, bool) {
	for _, oid := range s.order {
		if oid == id {
			return oid, true
		}
	}
	return "", false
}

// evictLocked removes the oldest checkpoint files beyond the capacity limit.
// Caller must hold s.mu.
func (s *FileSystemCheckpointStore) evictLocked() {
	if s.max <= 0 {
		return
	}
	for len(s.order) > s.max {
		oldest := s.order[0]
		s.order = s.order[1:]
		path := filepath.Join(s.dir, oldest+".ckpt")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			// eviction must not fail the Save; log-free best effort
			continue
		}
	}
}

// Load reads and decodes the checkpoint file with the given id.
//
// Load 读取并解码指定 id 的 checkpoint 文件。
func (s *FileSystemCheckpointStore) Load(_ context.Context, id string) (*types.Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.dir, id+".ckpt")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint %q: %w", id, err)
	}
	var cp types.Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("unmarshal checkpoint %q: %w", id, err)
	}
	return &cp, nil
}

// Delete removes the checkpoint file with the given id.
//
// Delete 删除指定 id 的 checkpoint 文件。
func (s *FileSystemCheckpointStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, id+".ckpt")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete checkpoint %q: %w", id, err)
	}
	return nil
}

// List returns the sorted ids of all checkpoint files in the directory.
//
// List 返回目录中所有 checkpoint 文件的排序后的 id 列表。
func (s *FileSystemCheckpointStore) List(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ckpt") {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".ckpt"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}
