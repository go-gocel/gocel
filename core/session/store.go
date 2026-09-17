// Package session 的持久化缝：Store 契约与两个实现。机制本身不落盘——
// 产品选择后端（文件、数据库、远程）。语义对齐 DSH session-persistence：
//
//   - 整日志替换式保存：Save 把完整事件列表原子地持久化到 id 下，
//     替换先前内容（append-only 日志的物理快照，不是增量补丁）。
//   - 读取必须返回按 seq 排序的完整列表；损坏或版本不识别必须报错
//     （fail-loud），绝不静默裁剪。
//   - 实现必须并发安全。
package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/go-gocel/gocel/core/types"
)

// ErrNotFound is returned by Store.Load for an unknown id.
//
// ErrNotFound 在 Load 未知 id 时返回。
var ErrNotFound = errors.New("session: log not found")

// Store persists complete session logs by id. Implementations must be
// safe for concurrent use and must persist atomically (a crash leaves
// either the old or the new document, never a torn one).
//
// Store 按 id 持久化完整会话日志。实现必须并发安全且原子持久化
// （崩溃只留下旧或新文档，绝不撕裂）。
type Store interface {
	// Save persists the complete event list under id, replacing any
	// previous document. The list must be seq-sorted on load.
	Save(ctx context.Context, id string, events []types.SessionEvent) error
	// Load returns the complete seq-sorted event list, or ErrNotFound.
	Load(ctx context.Context, id string) ([]types.SessionEvent, error)
	// List returns all persisted ids, sorted.
	List(ctx context.Context) ([]string, error)
	// Delete removes the document; deleting a missing id is a no-op.
	Delete(ctx context.Context, id string) error
}

// MemoryStore keeps logs in process memory. It is the default for tests
// and single-process products; durable products mount a file or database
// backend.
//
// MemoryStore 在进程内存中保存日志。它是测试与单进程产品的默认实现；
// 持久化产品挂载文件或数据库后端。
type MemoryStore struct {
	mu    sync.Mutex
	items map[string][]types.SessionEvent
}

// NewMemoryStore creates an empty memory store.
// NewMemoryStore 创建空的内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string][]types.SessionEvent)}
}

// Save implements Store: stores a copy of the events under id.
// Save 实现 Store 接口：在 id 下保存事件列表的副本（内存实现）。
func (s *MemoryStore) Save(_ context.Context, id string, events []types.SessionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]types.SessionEvent(nil), events...)
	s.items[id] = cp
	return nil
}

// Load implements Store: returns the stored events, or ErrNotFound.
// Load 实现 Store 接口：返回已存储的事件列表，未知 id 返回 ErrNotFound。
func (s *MemoryStore) Load(_ context.Context, id string) ([]types.SessionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, ok := s.items[id]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]types.SessionEvent(nil), evs...), nil
}

// List implements Store: returns all stored ids, sorted.
// List 实现 Store 接口：返回全部已存储 id，按序排列。
func (s *MemoryStore) List(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// Delete implements Store: removes the document; deleting a missing id
// is a no-op.
// Delete 实现 Store 接口：删除文档；删除不存在的 id 是空操作。
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return nil
}

// FileStore persists each log as one JSON document under a directory. It
// writes atomically (temp file + rename in the same directory), so a crash
// leaves the previous document intact.
//
// FileStore 把每条日志保存为目录下的一个 JSON 文档。写入原子化
// （同目录临时文件 + rename），崩溃保留先前文档。
type FileStore struct {
	dir string
}

// NewFileStore creates the store, creating the directory if needed.
// NewFileStore 创建存储，必要时会创建目录。
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: create store dir: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

// path resolves the document path for an id. The id is injectively
// encoded to a single safe path segment (base64url of its UTF-8 bytes) —
// reversible, immune to traversal and filesystem separators.
func (s *FileStore) path(id string) (string, error) {
	if id == "" {
		return "", errors.New("session: empty id")
	}
	return filepath.Join(s.dir, encodeSegment(id)+".json"), nil
}

// Save implements Store: atomically persists the events as one JSON
// document (temp file + rename).
// Save 实现 Store 接口：将事件原子化持久化为一个 JSON 文档（临时文件 +
// rename）。
func (s *FileStore) Save(_ context.Context, id string, events []types.SessionEvent) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	data, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("session: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("session: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("session: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("session: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("session: rename: %w", err)
	}
	return nil
}

// Load implements Store: reads and validates the document, or returns
// ErrNotFound for a missing id.
// Load 实现 Store 接口：读取并校验文档，id 不存在时返回 ErrNotFound。
func (s *FileStore) Load(_ context.Context, id string) ([]types.SessionEvent, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("session: read: %w", err)
	}
	var evs []types.SessionEvent
	if err := json.Unmarshal(data, &evs); err != nil {
		return nil, fmt.Errorf("session: corrupt document %q: %w", id, err)
	}
	// Validate: seqs must be contiguous ascending — the load contract.
	for i := range evs {
		if evs[i].Seq != int64(i+1) {
			return nil, fmt.Errorf("session: corrupt document %q: seq[%d]=%d, want %d", id, i, evs[i].Seq, i+1)
		}
	}
	return evs, nil
}

// List implements Store: returns the ids of the store's own documents,
// sorted.
// List 实现 Store 接口：返回本存储自有文档的 id，按序排列。
func (s *FileStore) List(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		id, err := decodeSegment(name[:len(name)-len(".json")])
		if err != nil {
			// A foreign file in the store directory is not ours; skip it
			// silently (the store owns only its own documents).
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// Delete implements Store: removes the document; deleting a missing id
// is a no-op.
// Delete 实现 Store 接口：删除文档；删除不存在的 id 是空操作。
func (s *FileStore) Delete(_ context.Context, id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// encodeSegment maps an arbitrary id to a single safe path segment:
// base64url of its UTF-8 bytes — injective and reversible, immune to
// traversal and filesystem separators (DSH injective escaping).
func encodeSegment(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

// decodeSegment reverses encodeSegment. A foreign (non-base64url) segment
// is reported as an error so List skips files it does not own.
func decodeSegment(seg string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
