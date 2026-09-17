package multiedit

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

func TestMultiEditToolName(t *testing.T) {
	p := New()
	tools := p.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "multi_edit" {
		t.Errorf("expected name 'multi_edit', got %q", tools[0].Name())
	}
}

// TestMultiEdit_StaleVersionRejected: an edit carrying an expect_version
// that no longer matches rejects the edit and leaves the file untouched.
func TestMultiEdit_StaleVersionRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "hello", "new_str": "goodbye", "expect_version": "stale"},
		},
	})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("multi_edit = %v, want a stale-version file result", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Status != "error" && !strings.Contains(result, "stale version") {
		t.Fatalf("result must report the stale rejection, got %q", result)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Fatalf("stale edit must not mutate the file, got %q", string(data))
	}
}

// TestMultiEdit_FreshVersionPasses: an edit whose expect_version matches
// the file's current fingerprint applies normally.
func TestMultiEdit_FreshVersionPasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	v, err := toolutil.FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "hello", "new_str": "goodbye", "expect_version": v},
		},
	})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("multi_edit = %v, want nil", err)
	}
	if !strings.Contains(result, `"status":"ok"`) {
		t.Fatalf("fresh edit must succeed, got %q", result)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "goodbye world" {
		t.Fatalf("edited = %q, want goodbye world", string(data))
	}
}

func TestMultiEdit_PerFileEdit(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "file1.txt")
	path2 := filepath.Join(dir, "file2.txt")
	if err := os.WriteFile(path1, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path2, []byte("foo bar"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path1, "old_str": "hello", "new_str": "goodbye"},
			{"path": path2, "old_str": "foo", "new_str": "baz"},
		},
	})
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
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	success, _ := data["success"].(float64)
	if success != 2 {
		t.Errorf("expected 2 successes, got %v", success)
	}

	// Verify file contents
	data1, _ := os.ReadFile(path1)
	if string(data1) != "goodbye world" {
		t.Errorf("file1: expected 'goodbye world', got %q", string(data1))
	}
	data2, _ := os.ReadFile(path2)
	if string(data2) != "baz bar" {
		t.Errorf("file2: expected 'baz bar', got %q", string(data2))
	}
}

func TestMultiEdit_PerFileEditOldStrNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "nonexistent", "new_str": "replacement"},
		},
	})
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
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	failed, _ := data["failed"].(float64)
	if failed != 1 {
		t.Errorf("expected 1 failure, got %v", failed)
	}
}

func TestMultiEdit_CrossFileMode(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "foo.txt")
	path2 := filepath.Join(dir, "bar.txt")
	if err := os.WriteFile(path1, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path2, []byte("hello universe"), 0644); err != nil {
		t.Fatal(err)
	}

	// Change to the temp dir so the walk in runCrossFile starts from "."
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"pattern": "*.txt",
		"old_str": "hello",
		"new_str": "hi",
	})
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
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	success, _ := data["success"].(float64)
	if success != 2 {
		t.Errorf("expected 2 successes, got %v", success)
	}

	data1, _ := os.ReadFile(path1)
	if string(data1) != "hi world" {
		t.Errorf("foo.txt: expected 'hi world', got %q", string(data1))
	}
}

func TestMultiEdit_CrossFileNoMatch(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"pattern": "*.txt",
		"old_str": "nothing",
		"new_str": "replacement",
	})
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
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	total, _ := data["total"].(float64)
	if total != 0 {
		t.Errorf("expected 0 total files, got %v", total)
	}
}

func TestMultiEdit_CrossFileNoOldStr(t *testing.T) {
	tool := &multiEditTool{}
	_, err := tool.Run(context.Background(), `{"pattern":"*.txt"}`)
	if err == nil {
		t.Error("expected error when old_str is missing with pattern, got nil")
	}
}

func TestMultiEdit_RegexMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello 123 world 456"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "\\d+", "new_str": "X"},
		},
		"regex": true,
	})
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

	data, _ := os.ReadFile(path)
	content := string(data)
	if strings.Contains(content, "123") {
		t.Error("regex replacement did not replace digits")
	}
	if !strings.Contains(content, "hello X world 456") && !strings.Contains(content, "hello X world X") {
		t.Errorf("unexpected content: %q", content)
	}
}

func TestMultiEdit_RegexCrossFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	if err := os.WriteFile(path, []byte("error: something failed\ninfo: all good"), 0644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"pattern": "*.log",
		"old_str": "error:.*",
		"new_str": "ERROR: matched",
		"regex":   true,
	})
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
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	success, _ := data["success"].(float64)
	if success != 1 {
		t.Errorf("expected 1 success, got %v", success)
	}

	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "ERROR: matched") {
		t.Errorf("expected 'ERROR: matched' in result, got %q", string(content))
	}
}

func TestMultiEdit_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "hello world"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "hello", "new_str": "goodbye"},
		},
		"dry_run": true,
	})
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
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	dryRun, _ := data["dry_run"].(bool)
	if !dryRun {
		t.Error("expected dry_run=true in data")
	}
	success, _ := data["success"].(float64)
	if success != 1 {
		t.Errorf("expected 1 success, got %v", success)
	}

	// File should remain unchanged
	content, _ := os.ReadFile(path)
	if string(content) != original {
		t.Errorf("file should be unchanged in dry run, got %q", string(content))
	}
}

func TestMultiEdit_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("a b a b a"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "a", "new_str": "X"},
		},
		"replace_all": true,
	})
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
	content, _ := os.ReadFile(path)
	if string(content) != "X b X b X" {
		t.Errorf("expected 'X b X b X', got %q", string(content))
	}
}

func TestMultiEdit_ReplaceFirstOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("a b c"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "a", "new_str": "X"},
		},
		// replace_all defaults to false
	})
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
	content, _ := os.ReadFile(path)
	if string(content) != "X b c" {
		t.Errorf("expected 'X b c' (first only), got %q", string(content))
	}
}

func TestMultiEdit_ReplaceMultipleFailsWithoutReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("a b a b a"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "a", "new_str": "X"},
		},
		// replace_all defaults to false
	})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	failed, _ := data["failed"].(float64)
	if failed != 1 {
		t.Errorf("expected 1 failure when multiple occurrences without replace_all, got %v", failed)
	}
}

func TestMultiEdit_ReplaceAllCrossFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(path, []byte("a a a"), 0644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"pattern":     "*.txt",
		"old_str":     "a",
		"new_str":     "X",
		"replace_all": true,
	})
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
	content, _ := os.ReadFile(path)
	if string(content) != "X X X" {
		t.Errorf("expected 'X X X', got %q", string(content))
	}
}

func TestMultiEdit_InvalidArgs(t *testing.T) {
	tool := &multiEditTool{}
	_, err := tool.Run(context.Background(), `bad json`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestMultiEdit_NoEditsNoPattern(t *testing.T) {
	tool := &multiEditTool{}
	_, err := tool.Run(context.Background(), `{}`)
	if err == nil {
		t.Error("expected error when neither edits nor pattern provided, got nil")
	}
	if !strings.Contains(err.Error(), "provide either") {
		t.Errorf("expected error containing 'provide either', got %v", err)
	}
}

func TestMultiEdit_FileNotFound(t *testing.T) {
	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": filepath.Join(t.TempDir(), "nonexistent.txt"), "old_str": "a", "new_str": "b"},
		},
	})
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
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	failed, _ := data["failed"].(float64)
	if failed != 1 {
		t.Errorf("expected 1 failure, got %v", failed)
	}
}

func TestMultiEdit_InvalidRegex(t *testing.T) {
	tool := &multiEditTool{}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "[invalid", "new_str": "x"},
		},
		"regex": true,
	})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	failed, _ := data["failed"].(float64)
	if failed != 1 {
		t.Errorf("expected 1 failure for invalid regex, got %v", failed)
	}
}

// ---------------------------------------------------------------------------
// Single-file edit (multi_edit now replaces edit)
// ---------------------------------------------------------------------------

func TestMultiEdit_SingleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "single.go")
	original := `package main

func main() {
	println("hello")
}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "hello", "new_str": "world"},
		},
	})
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
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "hello") {
		t.Errorf("'hello' was not replaced")
	}
	if !strings.Contains(string(data), "world") {
		t.Errorf("'world' was not inserted")
	}
}

// ---------------------------------------------------------------------------
// Preview mode (per-file) — unified diff output
// ---------------------------------------------------------------------------

func TestMultiEdit_Preview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.txt")
	original := "line1\nline2\nline3\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "line2", "new_str": "modified"},
		},
		"preview": true,
	})
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
	data := tr.Data.(map[string]any)

	// preview flag in result
	if data["preview"] != true {
		t.Error("expected preview=true in result")
	}

	// Each file result should have a preview field
	results := data["results"].([]any)
	r0 := results[0].(map[string]any)
	preview, ok := r0["preview"].(string)
	if !ok {
		t.Fatal("expected preview string in file result")
	}
	if !strings.Contains(preview, "-line2") {
		t.Errorf("preview should show '-line2', got: %s", preview)
	}
	if !strings.Contains(preview, "+modified") {
		t.Errorf("preview should show '+modified', got: %s", preview)
	}
	if !strings.Contains(preview, "---") || !strings.Contains(preview, "+++") {
		t.Errorf("preview should have diff headers, got: %s", preview)
	}

	// File must NOT be modified in preview mode
	content, _ := os.ReadFile(path)
	if string(content) != original {
		t.Error("file should not be modified in preview mode")
	}
}

// ---------------------------------------------------------------------------
// Preview mode (cross-file)
// ---------------------------------------------------------------------------

func TestMultiEdit_PreviewCrossFile(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(path1, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path2, []byte("world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"pattern": "*.txt",
		"old_str": "hello",
		"new_str": "hi",
		"preview": true,
	})
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
	data := tr.Data.(map[string]any)
	if data["preview"] != true {
		t.Error("expected preview=true")
	}

	// File must not be modified
	content1, _ := os.ReadFile(path1)
	if string(content1) != "hello\n" {
		t.Error("a.txt should be unchanged in preview")
	}
	content2, _ := os.ReadFile(path2)
	if string(content2) != "world\n" {
		t.Error("b.txt should be unchanged in preview")
	}
}

// ---------------------------------------------------------------------------
// Per-edit replace_all
// ---------------------------------------------------------------------------

func TestMultiEdit_PerEditReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "per_edit.txt")
	original := "x: hello\ny: hello\nz: world\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "hello", "new_str": "hi", "replace_all": true},
			{"path": path, "old_str": "world", "new_str": "earth"},
		},
	})
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

	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "hello") {
		t.Error("'hello' should have been replaced (replace_all=true)")
	}
	if strings.Contains(string(content), "world") {
		t.Error("'world' should have been replaced")
	}
	if !strings.Contains(string(content), "earth") {
		t.Error("expected 'earth' in result")
	}
}

// ---------------------------------------------------------------------------
// Binary file protection
// ---------------------------------------------------------------------------

func TestMultiEdit_BinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	if err := os.WriteFile(path, []byte("hello\x00world"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "hello", "new_str": "world"},
		},
	})
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
	data := tr.Data.(map[string]any)
	failed, _ := data["failed"].(float64)
	if failed != 1 {
		t.Errorf("expected 1 failure for binary file, got %v", failed)
	}
}

// ---------------------------------------------------------------------------
// generatePreview unit tests
// ---------------------------------------------------------------------------

func TestGeneratePreview_NoChanges(t *testing.T) {
	out := generatePreview("same\ncontent\n", "same\ncontent\n", "f.go")
	if !strings.Contains(out, "no changes") {
		t.Errorf("expected 'no changes', got: %s", out)
	}
}

func TestGeneratePreview_Format(t *testing.T) {
	orig := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	newc := "package main\n\nfunc main() {\n\tprintln(\"world\")\n}\n"
	out := generatePreview(orig, newc, "main.go")

	if !strings.HasPrefix(out, "--- main.go\n+++ main.go\n") {
		t.Errorf("expected header, got: %s", out)
	}
	if !strings.Contains(out, `-	println("hello")`) {
		t.Errorf("expected '-' line, got: %s", out)
	}
	if !strings.Contains(out, `+	println("world")`) {
		t.Errorf("expected '+' line, got: %s", out)
	}
	if !strings.Contains(out, " func main() {") {
		t.Errorf("expected context, got: %s", out)
	}
	if !strings.Contains(out, " }") {
		t.Errorf("expected context '}', got: %s", out)
	}
}

func TestMultiEdit_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &multiEditTool{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args := makeArgs(map[string]any{
		"edits": []map[string]any{
			{"path": path, "old_str": "hello", "new_str": "world"},
		},
	})
	_, err := tool.Run(ctx, args)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}
