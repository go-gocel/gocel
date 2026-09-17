package grep

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
	if tools[0].Name() != "grep" {
		t.Errorf("expected name 'grep', got %q", tools[0].Name())
	}
}

func TestGrep_FindsMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findme.txt")
	if err := os.WriteFile(path, []byte("hello world\nthis is a test\nhello again"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &grepTool{}
	result, err := tool.Run(context.Background(), makeArgs(map[string]any{"pattern": "hello", "path": dir}))
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
	output, _ := data["output"].(string)
	if !strings.Contains(output, "hello") {
		t.Errorf("expected matches in data.output, got: %q", output)
	}
}

func TestGrep_NoMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("nothing here"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &grepTool{}
	result, err := tool.Run(context.Background(), makeArgs(map[string]any{"pattern": "zzz_not_found", "path": dir}))
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
	if !strings.Contains(tr.Message, "no matches") {
		t.Logf("no-match message: %q", tr.Message)
	}
	data := tr.Data.(map[string]any)
	if m, _ := data["matches"].(float64); m != 0 {
		t.Errorf("expected 0 matches, got %v", m)
	}
}

func TestGrep_EmptyPattern(t *testing.T) {
	tool := &grepTool{}
	_, err := tool.Run(context.Background(), makeArgs(map[string]any{"pattern": "", "path": "."}))
	if err == nil {
		t.Error("expected error for empty pattern, got nil")
	}
}

func TestGrep_InvalidArgs(t *testing.T) {
	tool := &grepTool{}
	_, err := tool.Run(context.Background(), `bad json`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
