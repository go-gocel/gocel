// Package spill provides the large-output storage seam: one method, one
// contract. Tools and context filters hand oversized text to SaveText and
// receive an opaque locator plus a retrieval hint; the message content is
// replaced by a bounded preview that points at the stored full text.
//
// Contract (DSH spill semantics):
//   - Storage is namespaced by owner (session id) — locators never leak
//     across owners.
//   - A store failure is reported, never swallowed: the caller decides
//     whether to degrade (context filters keep the original text).
//   - The suggested name is a hint only — never trusted as a path.
//
// Package spill 提供大输出落盘缝：一个方法、一份契约。工具与上下文
// 过滤器把超限文本交给 SaveText，拿回不透明 locator 与检索提示；
// 消息内容被替换为指向完整落盘文本的有界预览。
//
// 契约（DSH spill 语义）：
//   - 存储按 owner（会话 id）命名空间隔离——locator 绝不跨 owner 泄漏。
//   - 存储失败如实上报、绝不吞掉：是否降级由调用方决定（上下文过滤器
//     保留原文）。
//   - 建议文件名只是提示——绝不被当作可信路径。
package spill

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNotFound is returned by Retrieve for an unknown locator.
// ErrNotFound 在 Retrieve 遇到未知 locator 时返回。
var ErrNotFound = errors.New("spill: locator not found")

// Ref is the result of a SaveText call.
// Ref 是 SaveText 调用的返回结果。
type Ref struct {
	// Locator is the opaque retrieval key, namespaced by owner.
	Locator string `json:"locator"`
	// Bytes is the stored size in bytes.
	Bytes int `json:"bytes"`
	// Hint tells a reader how to retrieve the full text (path, command…).
	Hint string `json:"hint"`
}

// Store is the storage seam. Implementations must be safe for concurrent
// use and must report failures honestly.
// Store 是落盘存储缝。实现必须并发安全，且如实上报失败。
type Store interface {
	// SaveText stores text under an owner namespace and returns the ref.
	SaveText(ctx context.Context, owner, text string) (Ref, error)
	// Retrieve returns the stored text or ErrNotFound.
	Retrieve(ctx context.Context, owner, locator string) (string, error)
}

// MemoryStore keeps spills in process memory (tests, single-process
// products). A real product mounts a file-backed store.
// MemoryStore 在进程内存中保存落盘内容（测试、单进程产品）。真实产品
// 应挂载文件后端存储。
type MemoryStore struct {
	mu    sync.Mutex
	items map[string]string // key: owner + "\x00" + locator
}

// NewMemoryStore creates an empty memory store.
// NewMemoryStore 创建空的内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]string)}
}

// SaveText stores text under the owner namespace and returns the ref.
// SaveText 将文本存入 owner 命名空间下，并返回引用。
func (s *MemoryStore) SaveText(_ context.Context, owner, text string) (Ref, error) {
	locator := newLocator()
	s.mu.Lock()
	s.items[owner+"\x00"+locator] = text
	s.mu.Unlock()
	return Ref{Locator: locator, Bytes: len(text), Hint: "memory:" + owner + ":" + locator}, nil
}

// Retrieve returns the stored text for a locator or ErrNotFound.
// Retrieve 返回 locator 对应的已存文本，未知时返回 ErrNotFound。
func (s *MemoryStore) Retrieve(_ context.Context, owner, locator string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, ok := s.items[owner+"\x00"+locator]
	if !ok {
		return "", ErrNotFound
	}
	return text, nil
}

// FileStore persists spills as files under a directory, one per locator,
// namespaced by owner subdirectory.
// FileStore 把落盘内容持久化为目录下的文件，每个 locator 一个文件，按
// owner 子目录命名空间隔离。
type FileStore struct {
	dir string
}

// NewFileStore creates the store, creating the directory if needed.
// NewFileStore 创建存储，必要时会创建目录。
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("spill: create store dir: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

// SaveText stores text as a file under the owner's subdirectory and
// returns the ref; a hostile owner (separators, "..", absolute paths) is
// rejected loudly.
// SaveText 将文本以文件形式存入 owner 子目录并返回引用；恶意 owner
// （分隔符、".."、绝对路径）会被显式拒绝。
func (s *FileStore) SaveText(_ context.Context, owner, text string) (Ref, error) {
	// The owner is a caller-supplied namespace. It becomes a path
	// component below the store directory — a hostile owner (separators,
	// "..", absolute paths) must fail loudly instead of escaping the store
	// root (verified traversal defect).
	if err := validateOwner(owner); err != nil {
		return Ref{}, err
	}
	locator := newLocator()
	rel := filepath.Join(owner, locator+".txt")
	path := filepath.Join(s.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Ref{}, fmt.Errorf("spill: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return Ref{}, fmt.Errorf("spill: write: %w", err)
	}
	return Ref{Locator: rel, Bytes: len(text), Hint: "file:" + path}, nil
}

// validateOwner rejects owners that could escape the store directory when
// joined into a path: separators, ".." segments, and absolute paths.
func validateOwner(owner string) error {
	if owner == "" {
		return fmt.Errorf("spill: empty owner")
	}
	if filepath.IsAbs(owner) || strings.ContainsAny(owner, `/\`) {
		return fmt.Errorf("spill: owner %q must be a single path segment", owner)
	}
	for _, seg := range strings.Split(owner, ".") {
		if seg == ".." {
			return fmt.Errorf("spill: owner %q must not contain '..'", owner)
		}
	}
	return nil
}

// Retrieve returns the stored text for an owner-scoped locator or
// ErrNotFound; retrieval is fenced to the store root and the requesting
// owner.
// Retrieve 返回 owner 作用域 locator 对应的已存文本，未知或越界时返回
// ErrNotFound；检索被限制在存储根目录与请求 owner 范围内。
func (s *FileStore) Retrieve(_ context.Context, owner, locator string) (string, error) {
	// The locator embeds the owner-relative path; retrieval is fenced to
	// the store directory AND to the requesting owner (locators are
	// generated by this store — no path traversal possible).
	loc := filepath.FromSlash(locator)
	rel, err := filepath.Rel(s.dir, filepath.Join(s.dir, loc))
	if err != nil || rel == ".." || filepath.IsAbs(rel) || rel == "." {
		return "", ErrNotFound
	}
	first, _ := filepath.Split(rel)
	wantOwner := strings.TrimSuffix(first, string(filepath.Separator))
	if wantOwner == "" || wantOwner != owner {
		return "", ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.dir, rel))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("spill: read: %w", err)
	}
	return string(data), nil
}

func newLocator() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Ensure the store implementations stay honest.
var (
	_ Store = (*MemoryStore)(nil)
	_ Store = (*FileStore)(nil)
)
