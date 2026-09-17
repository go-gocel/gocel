package write

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/internal/toolutil"
)

// makeArgs builds a JSON string from a map, properly escaping values.
func makeArgs(m map[string]any) string {
	data, _ := json.Marshal(m)
	return string(data)
}

func TestToolName(t *testing.T) {
	p := New()
	tools := p.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "write" {
		t.Errorf("expected name 'write', got %q", tools[0].Name())
	}
}

// TestWriteFile_ReturnsVersionFingerprint: a successful write reports the
// file's version fingerprint so a later guarded edit can verify freshness.
func TestWriteFile_ReturnsVersionFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.txt")
	tool := &writeFileTool{}
	result, err := tool.Run(context.Background(), makeArgs(map[string]any{"path": path, "content": "v1"}))
	if err != nil {
		t.Fatal(err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatal(err)
	}
	data, ok := tr.Data.(map[string]any)
	if !ok || data["version"] == nil || data["version"] == "" {
		t.Fatalf("write result must carry a version fingerprint, got %+v", tr.Data)
	}
}

// TestWriteFile_StaleVersionRejected: writing with an expect_version that
// no longer matches rejects the write and leaves the file untouched.
func TestWriteFile_StaleVersionRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &writeFileTool{}

	// Fresh expectation: the write succeeds and reports the new version.
	out, err := tool.Run(context.Background(), makeArgs(map[string]any{
		"path": path, "content": "edited", "expect_version": "bogus",
	}))
	if err == nil {
		t.Fatalf("stale write = nil (%s), want rejection", out)
	}
	if !toolutil.IsStaleVersion(err) {
		t.Fatalf("stale error = %T %v, want StaleVersionError", err, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatalf("stale write must not mutate the file, got %q", string(data))
	}
}

// TestWriteFile_CreateIfAbsent: the create_if_absent guard refuses to
// clobber an existing file.
func TestWriteFile_CreateIfAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &writeFileTool{}
	if _, err := tool.Run(context.Background(), makeArgs(map[string]any{
		"path": path, "content": "new", "create_if_absent": true,
	})); err == nil {
		t.Fatal("create_if_absent on an existing file must be rejected")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "existing" {
		t.Fatalf("create_if_absent must not mutate the file, got %q", string(data))
	}
	// On an absent file it creates normally.
	absent := filepath.Join(dir, "absent.txt")
	if _, err := tool.Run(context.Background(), makeArgs(map[string]any{
		"path": absent, "content": "new", "create_if_absent": true,
	})); err != nil {
		t.Fatalf("create_if_absent on an absent file = %v, want nil", err)
	}
}

func TestWriteFile_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	args := makeArgs(map[string]any{"path": path, "content": "hello world"})

	tool := &writeFileTool{}
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", tr.Status)
	}
	if !strings.Contains(tr.Message, "written to") || !strings.Contains(tr.Message, "11") {
		t.Errorf("unexpected message: %q", tr.Message)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "nested", "test.txt")
	args := makeArgs(map[string]any{"path": path, "content": "nested"})

	tool := &writeFileTool{}
	_, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("file was not created at nested path")
	}
}

func TestWriteFile_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	args := makeArgs(map[string]any{"path": path, "content": ""})

	tool := &writeFileTool{}
	_, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(data))
	}
}

func TestWriteFile_InvalidArgs(t *testing.T) {
	tool := &writeFileTool{}
	_, err := tool.Run(context.Background(), `{invalid json}`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestWriteFile_MissingPath(t *testing.T) {
	tool := &writeFileTool{}
	_, err := tool.Run(context.Background(), `{"content":"hello"}`)
	if err == nil {
		t.Error("expected error for missing path, got nil")
	}
}
