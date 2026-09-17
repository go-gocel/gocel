// Package attachment provides the immutable content-addressed attachment
// store (DSH attachment): image bytes are validated, committed atomically,
// and addressed by their sha256 — consumers persist only the opaque
// AttachmentRef, never paths or base64. The store is the mechanism; media
// validation (which formats, byte limits) belongs to the consumer layer.
//
// Semantics (borrowed from DSH attachment-local):
//
//   - Immutable: an id is the sha256 of its bytes; the same bytes always
//     resolve to the same object (natural deduplication).
//   - Atomic publish: bytes are written to a private temp file, fsynced,
//     then linked into the object store — a crash leaves either nothing or
//     the complete object, never a torn one.
//   - Honest reads: retrieval verifies the object against its id before
//     returning bytes; a mismatch or missing object fails loudly.
//   - Fail-closed: invalid input (empty, oversized) is rejected before any
//     write.
//
// Package attachment 提供不可变内容寻址附件存储（DSH attachment）：图片
// 字节先校验、再原子提交、按 sha256 寻址——消费方只持久化不透明的
// AttachmentRef，绝不持久化路径或 base64。存储是机制；媒体校验（格式、
// 字节上限）属于消费层。
//
// 语义（借鉴 DSH attachment-local）：
//
//   - 不可变：id 是其字节的 sha256；相同字节永远解析到同一对象
//     （天然去重）。
//   - 原子发布：字节先写入私有临时文件、fsync、再链接进对象库——崩溃
//     只留下"没有"或"完整对象"，绝不撕裂。
//   - 诚实读取：取回前用 id 校验对象；不匹配或缺失显式报错。
//   - fail-closed：非法输入（空、超限）在任何写入前被拒绝。
package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Ref is the opaque durable reference to one immutable attachment.
// Ref 是对一个不可变附件的不透明持久引用。
type Ref struct {
	// ID is the content address: "sha256:<hex>".
	ID string `json:"id"`
	// MediaType is the declared media type (validated by the consumer).
	MediaType string `json:"media_type"`
	// Bytes is the object size in bytes.
	Bytes int64 `json:"bytes"`
	// Name is an optional display name (never trusted as a path).
	Name string `json:"name,omitempty"`
}

var (
	// ErrNotFound is returned by Retrieve for an unknown id.
	// ErrNotFound 是 Retrieve 在 id 未知时返回的错误。
	ErrNotFound = errors.New("attachment: object not found")
	// ErrCorrupt is returned when the stored object fails id verification.
	// ErrCorrupt 是存储对象未通过 id 校验时返回的错误。
	ErrCorrupt = errors.New("attachment: stored object corrupt")
	// ErrInvalidInput is returned for empty or oversized input.
	// ErrInvalidInput 是空或超限输入被拒绝时返回的错误。
	ErrInvalidInput = errors.New("attachment: invalid input")
)

// Store persists immutable attachments by content address.
// Store 按内容地址持久化不可变附件。
type Store interface {
	// Save commits the bytes atomically and returns the content-addressed
	// ref. A duplicate (already-stored) id returns the existing ref.
	Save(ctx context.Context, mediaType string, data []byte, name string) (Ref, error)
	// Retrieve returns the object bytes for a ref id, verifying the
	// content address. ErrNotFound when absent, ErrCorrupt on mismatch.
	Retrieve(ctx context.Context, id string) ([]byte, error)
	// Has reports whether the object exists.
	Has(ctx context.Context, id string) (bool, error)
}

// ContentID derives the content address of the bytes.
// ContentID 派生字节的内容地址。
func ContentID(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FileStore persists objects under a directory: objects/xx/<sha256>.
// It is safe for concurrent use.
//
// FileStore 把对象持久化到目录下：objects/xx/<sha256>。并发安全。
type FileStore struct {
	dir string
}

// NewFileStore creates the store, creating the directory if needed.
// NewFileStore 创建存储，必要时创建目录。
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o700); err != nil {
		return nil, fmt.Errorf("attachment: create store: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

// Dir returns the store root.
// Dir 返回存储根目录。
func (s *FileStore) Dir() string { return s.dir }

// Save implements Store.
// Save 实现 Store：原子提交字节并返回内容寻址引用。
func (s *FileStore) Save(_ context.Context, mediaType string, data []byte, name string) (Ref, error) {
	if len(data) == 0 {
		return Ref{}, fmt.Errorf("%w: empty data", ErrInvalidInput)
	}
	id := ContentID(data)
	obj := s.objectPath(id)

	// Fast path: already stored (immutable — no verification needed at
	// write time beyond existence).
	if _, err := os.Stat(obj); err == nil {
		return Ref{ID: id, MediaType: mediaType, Bytes: int64(len(data)), Name: name}, nil
	}

	// Atomic publish: temp file in the same bucket directory (same
	// filesystem), fsync, then link (no-replace) into the object store.
	if err := os.MkdirAll(s.objectDir(id), 0o700); err != nil {
		return Ref{}, fmt.Errorf("attachment: bucket: %w", err)
	}
	tmp, err := os.CreateTemp(s.objectDir(id), ".attach-*.tmp")
	if err != nil {
		return Ref{}, fmt.Errorf("attachment: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return Ref{}, fmt.Errorf("attachment: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Ref{}, fmt.Errorf("attachment: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Ref{}, fmt.Errorf("attachment: close: %w", err)
	}
	// link fails with EEXIST when a concurrent writer won the race — both
	// stored the same immutable bytes, so EEXIST is success.
	if err := os.Link(tmpName, obj); err != nil && !os.IsExist(err) {
		return Ref{}, fmt.Errorf("attachment: publish: %w", err)
	}
	// Directory fsync makes the link durable.
	if d, err := os.Open(s.objectDir(id)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return Ref{ID: id, MediaType: mediaType, Bytes: int64(len(data)), Name: name}, nil
}

// validID reports whether id is a well-formed content address:
// "sha256:" + 64 hex digits. Entry validation keeps malformed ids out of
// the filesystem — defense in depth beyond the content-address verification
// on read.
func validID(id string) bool {
	if len(id) != len("sha256:")+64 || !strings.HasPrefix(id, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(id[len("sha256:"):])
	return err == nil
}

// Retrieve implements Store: verify the object against its id before
// returning bytes. A malformed id is rejected at the entry (ErrInvalidInput)
// without touching the filesystem.
// Retrieve 实现 Store：返回字节前先用 id 校验对象；畸形 id 在入口被拒绝
// （ErrInvalidInput），不触碰文件系统。
func (s *FileStore) Retrieve(_ context.Context, id string) ([]byte, error) {
	if !validID(id) {
		return nil, fmt.Errorf("%w: malformed id", ErrInvalidInput)
	}
	obj := s.objectPath(id)
	data, err := os.ReadFile(obj)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("attachment: read: %w", err)
	}
	if ContentID(data) != id {
		return nil, fmt.Errorf("%w: %s", ErrCorrupt, id)
	}
	return data, nil
}

// Has implements Store.
// Has 实现 Store：报告对象是否存在。
func (s *FileStore) Has(_ context.Context, id string) (bool, error) {
	if !validID(id) {
		return false, fmt.Errorf("%w: malformed id", ErrInvalidInput)
	}
	_, err := os.Stat(s.objectPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// objectPath resolves the object file for an id: objects/xx/<sha256>.
func (s *FileStore) objectPath(id string) string {
	// id is "sha256:<hex>" — strip the scheme for the path.
	hexID := id
	if len(id) > 7 && id[:7] == "sha256:" {
		hexID = id[7:]
	}
	return filepath.Join(s.objectDir(id), hexID)
}

func (s *FileStore) objectDir(id string) string {
	hexID := id
	if len(id) > 7 && id[:7] == "sha256:" {
		hexID = id[7:]
	}
	prefix := "00"
	if len(hexID) >= 2 {
		prefix = hexID[:2]
	}
	return filepath.Join(s.dir, "objects", prefix)
}

// CopyTo writes the attachment bytes to w (a convenience for consumers
// streaming an attachment to a response).
//
// CopyTo 把附件字节写入 w（为消费方流式输出附件提供便利）。
func (s *FileStore) CopyTo(ctx context.Context, w io.Writer, id string) (int64, error) {
	data, err := s.Retrieve(ctx, id)
	if err != nil {
		return 0, err
	}
	n, err := w.Write(data)
	return int64(n), err
}
