package read

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

func TestToolName(t *testing.T) {
	p := New()
	tools := p.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "read" {
		t.Errorf("expected name 'read', got %q", tools[0].Name())
	}
}

func TestReadFile_SmallFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "line1\nline2\nline3"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &readFileTool{}
	result, err := tool.Run(context.Background(), makeArgs(map[string]any{"path": path}))
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
	if !strings.Contains(tr.Message, "3 lines") {
		t.Errorf("expected line count in message, got: %q", tr.Message)
	}
	data := tr.Data.(map[string]any)
	contentOut, _ := data["content"].(string)
	if !strings.Contains(contentOut, "line1") || !strings.Contains(contentOut, "line3") {
		t.Errorf("expected file content in data.content, got: %q", contentOut)
	}
}

func TestReadFile_WithOffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	content := "a\nb\nc\nd\ne"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &readFileTool{}
	result, err := tool.Run(context.Background(), makeArgs(map[string]any{"path": path, "offset": 2, "limit": 2}))
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
	contentOut, _ := data["content"].(string)
	if !strings.Contains(contentOut, "2→b") {
		t.Errorf("expected line 2 'b' in content, got: %q", contentOut)
	}
	if !strings.Contains(contentOut, "3→c") {
		t.Errorf("expected line 3 'c' in content, got: %q", contentOut)
	}
	if strings.Contains(contentOut, "4→d") {
		t.Errorf("line 4 should not be present with limit=2: %q", contentOut)
	}
}

func TestReadFile_NonExistent(t *testing.T) {
	tool := &readFileTool{}
	_, err := tool.Run(context.Background(), `{"path":"/nonexistent/path.txt"}`)
	if err == nil {
		t.Error("expected error for non-existent file, got nil")
	}
}

func TestReadFile_EmptyPath(t *testing.T) {
	tool := &readFileTool{}
	_, err := tool.Run(context.Background(), `{"path":""}`)
	if err == nil {
		t.Error("expected error for empty path, got nil")
	}
}

func TestReadFile_InvalidArgs(t *testing.T) {
	tool := &readFileTool{}
	_, err := tool.Run(context.Background(), `bad json`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestReadFile_OnlyOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	content := "1\n2\n3\n4\n5"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &readFileTool{}
	result, err := tool.Run(context.Background(), makeArgs(map[string]any{"path": path, "offset": 3}))
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
	contentOut, _ := data["content"].(string)
	if !strings.Contains(contentOut, "3→3") {
		t.Errorf("expected line 3 to be first: %q", contentOut)
	}
	if !strings.Contains(contentOut, "5→5") {
		t.Errorf("expected line 5 to be last: %q", contentOut)
	}
}

func TestParseReadArgs(t *testing.T) {
	args, err := parseReadArgs(`{"path":"test.go","offset":5,"limit":10}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.Path != "test.go" {
		t.Errorf("expected path 'test.go', got %q", args.Path)
	}
	if args.Offset != 5 {
		t.Errorf("expected offset 5, got %d", args.Offset)
	}
	if args.Limit != 10 {
		t.Errorf("expected limit 10, got %d", args.Limit)
	}
}

func TestParseReadArgs_EmptyPath(t *testing.T) {
	_, err := parseReadArgs(`{"path":""}`)
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestValidatePath_Directory(t *testing.T) {
	dir := t.TempDir()
	err := validatePath(dir)
	if err == nil {
		t.Error("expected error for directory path")
	}
}
