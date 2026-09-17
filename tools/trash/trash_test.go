package trash

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

// chdirTemp changes to a temp directory and returns a restore function.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chdir(orig)
	})
	return dir
}

func TestTrashToolName(t *testing.T) {
	p := New()
	tools := p.ListTools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name()
		if tool.Description() == "" {
			t.Errorf("tool %q has empty description", tool.Name())
		}
		if tool.Schema() == nil {
			t.Errorf("tool %q has nil schema", tool.Name())
		}
	}

	expected := []string{"trash", "restore_trash", "trash_list"}
	for _, name := range expected {
		found := false
		for _, n := range names {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected tool %q not found in tools: %v", name, names)
		}
	}
}

func TestTrash_TrashFile(t *testing.T) {
	dir := chdirTemp(t)

	// Create a file
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Trash it
	args := makeArgs(map[string]any{"path": filePath})
	tool := &trashTool{}
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

	// Original file should be gone
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("original file should be removed after trash")
	}

	// File should be in .gocode/.trash/
	trashDir := filepath.Join(dir, ".gocode", ".trash")
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		t.Fatalf("failed to read trash dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least 1 entry in trash")
	}

	// Manifest should exist
	manifestPath := filepath.Join(trashDir, ".manifest.jsonl")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Errorf("manifest file should exist at %s", manifestPath)
	}
}

func TestTrash_TrashList(t *testing.T) {
	dir := chdirTemp(t)

	// Create and trash a file
	filePath := filepath.Join(dir, "listme.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	trashTool := &trashTool{}
	args := makeArgs(map[string]any{"path": filePath})
	if _, err := trashTool.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}

	// List trash
	listTool := &trashListTool{}
	result, err := listTool.Run(context.Background(), `{}`)
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

	// Data should contain items
	if tr.Data != nil {
		m, ok := tr.Data.(map[string]any)
		if !ok {
			t.Fatalf("expected map in data, got %T", tr.Data)
		}
		total, _ := m["total"].(float64)
		if total < 1 {
			t.Errorf("expected at least 1 item in trash, got %v", total)
		}
	}
}

func TestTrash_Restore(t *testing.T) {
	dir := chdirTemp(t)

	// Create a file
	filePath := filepath.Join(dir, "restoreme.txt")
	if err := os.WriteFile(filePath, []byte("important"), 0644); err != nil {
		t.Fatal(err)
	}

	// Trash it
	trashTool := &trashTool{}
	args := makeArgs(map[string]any{"path": filePath})
	trashResult, err := trashTool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("trash failed: %v", err)
	}

	// Parse trash result to get the trash path
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(trashResult), &tr); err != nil {
		t.Fatalf("failed to unmarshal trash result: %v", err)
	}
	trashPath := ""
	if tr.Data != nil {
		m, _ := tr.Data.(map[string]any)
		if p, ok := m["trashed"].(string); ok {
			trashPath = p
		}
	}
	if trashPath == "" {
		t.Fatal("could not determine trash path from result")
	}

	// Verify original file is gone
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatal("original file still exists after trash")
	}

	// Restore it
	restoreTool := &restoreTrashTool{}
	restoreArgs := makeArgs(map[string]any{"path": trashPath})
	restoreResult, err := restoreTool.Run(context.Background(), restoreArgs)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	var restoreTr toolutil.ToolResult
	if err := json.Unmarshal([]byte(restoreResult), &restoreTr); err != nil {
		t.Fatalf("failed to unmarshal restore result: %v", err)
	}
	if restoreTr.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", restoreTr.Status)
	}

	// Original file should be back
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Errorf("file should be restored to original location")
	}

	// Trash entry should be gone
	trashDir := filepath.Join(dir, ".gocode", ".trash")
	entries, _ := os.ReadDir(trashDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "restoreme.txt") {
			t.Errorf("trash entry %q should be gone after restore", e.Name())
		}
	}
}

func TestTrash_PathNotExist(t *testing.T) {
	chdirTemp(t)

	args := makeArgs(map[string]any{"path": "/nonexistent/file.txt"})
	tool := &trashTool{}
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Error("expected error for non-existent path, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected 'does not exist' error, got: %v", err)
	}
}

func TestTrash_EmptyPath(t *testing.T) {
	tool := &trashTool{}
	_, err := tool.Run(context.Background(), `{}`)
	if err == nil {
		t.Error("expected error for empty args, got nil")
	}
}

func TestTrash_InvalidJSON(t *testing.T) {
	tool := &trashTool{}
	_, err := tool.Run(context.Background(), `bad json`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestTrash_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	chdirTemp(t)

	args := makeArgs(map[string]any{"path": "somefile.txt"})
	tool := &trashTool{}
	_, err := tool.Run(ctx, args)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

func TestTrash_RestoreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	args := makeArgs(map[string]any{"path": "/some/trash/path"})
	tool := &restoreTrashTool{}
	_, err := tool.Run(ctx, args)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

func TestTrash_RestoreMissingPath(t *testing.T) {
	tool := &restoreTrashTool{}
	_, err := tool.Run(context.Background(), `{}`)
	if err == nil {
		t.Error("expected error for missing path, got nil")
	}
}

func TestTrash_RestoreInvalidJSON(t *testing.T) {
	tool := &restoreTrashTool{}
	_, err := tool.Run(context.Background(), `bad json`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestTrash_TrashListEmpty(t *testing.T) {
	dir := chdirTemp(t)

	// Ensure .gocode/.trash does not exist
	trashDir := filepath.Join(dir, ".gocode", ".trash")
	os.RemoveAll(trashDir)

	listTool := &trashListTool{}
	result, err := listTool.Run(context.Background(), `{}`)
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
	if !strings.Contains(tr.Message, "empty") {
		t.Errorf("expected 'empty' message, got: %s", tr.Message)
	}
}
