package glob

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
	if tools[0].Name() != "glob" {
		t.Errorf("expected name 'glob', got %q", tools[0].Name())
	}
}

func TestGlob_FindsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("text"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &globTool{}
	result, err := tool.Run(context.Background(), makeArgs(map[string]any{"pattern": "*.go", "path": dir}))
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
	files, _ := data["files"].([]any)
	filesStr := make([]string, len(files))
	for i, f := range files {
		filesStr[i] = f.(string)
	}
	joined := strings.Join(filesStr, " ")
	if !strings.Contains(joined, "a.go") || !strings.Contains(joined, "b.go") {
		t.Errorf("expected go files in data.files, got: %v", filesStr)
	}
	if strings.Contains(joined, "c.txt") {
		t.Errorf("txt files should not be in glob result for *.go")
	}
}

func TestGlob_NoMatch(t *testing.T) {
	dir := t.TempDir()
	tool := &globTool{}
	result, err := tool.Run(context.Background(), makeArgs(map[string]any{"pattern": "*.xyz", "path": dir}))
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
	if !strings.Contains(tr.Message, "No files matched") && !strings.Contains(tr.Message, "0 matching") {
		t.Logf("no-match message: %q", tr.Message)
	}
	data := tr.Data.(map[string]any)
	files, _ := data["files"].([]any)
	if len(files) != 0 {
		t.Errorf("expected empty files array, got %v", files)
	}
}

func TestGlob_InvalidArgs(t *testing.T) {
	tool := &globTool{}
	_, err := tool.Run(context.Background(), `bad json`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestGlob_EmptyPattern(t *testing.T) {
	tool := &globTool{}
	_, err := tool.Run(context.Background(), makeArgs(map[string]any{"pattern": "", "path": "."}))
	if err == nil {
		t.Error("expected error for empty pattern, got nil")
	}
}
