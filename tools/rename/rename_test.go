package rename

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/internal/toolutil"
)

func makeArgs(m map[string]any) string {
	data, _ := json.Marshal(m)
	return string(data)
}

func TestRenameToolName(t *testing.T) {
	p := New()
	tools := p.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "rename" {
		t.Errorf("expected name 'rename', got %q", tools[0].Name())
	}
	if tools[0].Description() == "" {
		t.Error("expected non-empty description")
	}
	if tools[0].Schema() == nil {
		t.Error("expected non-nil schema")
	}
}

func TestRename_Single(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(oldPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	args := makeArgs(map[string]any{"old_path": oldPath, "new_path": newPath})
	tool := &renameTool{}
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

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old file should not exist after rename")
	}
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Errorf("new file should exist after rename")
	}
}

func TestRename_Batch(t *testing.T) {
	dir := t.TempDir()
	// Create files: foo_1.txt, foo_2.txt, foo_3.txt
	for i := 1; i <= 3; i++ {
		path := filepath.Join(dir, "foo_"+string(rune('0'+i))+".txt")
		if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Batch rename: pattern=*.txt, old_str=foo, new_str=bar
	args := makeArgs(map[string]any{
		"pattern": filepath.Join(dir, "*.txt"),
		"old_str": "foo",
		"new_str": "bar",
	})
	tool := &renameTool{}
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

	// Verify old files are gone and new files exist
	for i := 1; i <= 3; i++ {
		oldPath := filepath.Join(dir, "foo_"+string(rune('0'+i))+".txt")
		newPath := filepath.Join(dir, "bar_"+string(rune('0'+i))+".txt")
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Errorf("old file %q should not exist after rename", oldPath)
		}
		if _, err := os.Stat(newPath); os.IsNotExist(err) {
			t.Errorf("new file %q should exist after rename", newPath)
		}
	}
}

func TestRename_DryRun(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(oldPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	args := makeArgs(map[string]any{"old_path": oldPath, "new_path": newPath, "dry_run": true})
	tool := &renameTool{}
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

	// File must NOT be renamed in dry run
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		t.Errorf("old file should still exist during dry run")
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("new file should NOT exist during dry run")
	}
}

func TestRename_Force(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(oldPath, []byte("from"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	// Without force, should fail
	args := makeArgs(map[string]any{"old_path": oldPath, "new_path": newPath})
	tool := &renameTool{}
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Error("expected error without force, got nil")
	}

	// With force, should succeed
	args = makeArgs(map[string]any{"old_path": oldPath, "new_path": newPath, "force": true})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error with force: %v", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", tr.Status)
	}
}

func TestRename_MissingArgs(t *testing.T) {
	tool := &renameTool{}
	_, err := tool.Run(context.Background(), `{}`)
	if err == nil {
		t.Error("expected error for missing args, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "provide old_path+new_path") {
		t.Errorf("expected 'provide old_path+new_path' error, got: %v", err)
	}
}

func TestRename_NonExistentSource(t *testing.T) {
	args := makeArgs(map[string]any{"old_path": "/nonexistent/file.txt", "new_path": "/some/other.txt"})
	tool := &renameTool{}
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Error("expected error for non-existent source, got nil")
	}
}

func TestRename_InvalidJSON(t *testing.T) {
	tool := &renameTool{}
	_, err := tool.Run(context.Background(), `not json`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestRename_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(oldPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	args := makeArgs(map[string]any{"old_path": oldPath, "new_path": newPath})
	tool := &renameTool{}
	_, err := tool.Run(ctx, args)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}
