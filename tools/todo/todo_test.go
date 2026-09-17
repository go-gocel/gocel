package todo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/internal/toolutil"
)

func TestTodoToolName(t *testing.T) {
	p := New()
	tools := p.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "todo" {
		t.Errorf("expected name 'todo', got %q", tools[0].Name())
	}
}

func TestTodoToolDescription(t *testing.T) {
	p := New()
	desc := p.ListTools()[0].Description()
	if desc == "" {
		t.Error("description should not be empty")
	}
	if !strings.Contains(desc, "add") || !strings.Contains(desc, "update") || !strings.Contains(desc, "list") {
		t.Error("description should mention add, update, and list actions")
	}
}

func TestTodoSchema(t *testing.T) {
	p := New()
	schema := p.ListTools()[0].Schema()
	if schema == nil {
		t.Fatal("schema should not be nil")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema should have properties")
	}
	for _, field := range []string{"action", "content", "id", "status"} {
		if _, exists := props[field]; !exists {
			t.Errorf("schema should have field %q", field)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("schema should have required field")
	}
	if len(required) != 1 || required[0] != "action" {
		t.Errorf("required should be ['action'], got %v", required)
	}
}

func TestTodoAdd(t *testing.T) {
	tool := New().ListTools()[0]

	result, err := tool.Run(context.Background(), `{"action":"add","content":"Fix login bug"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("invalid JSON result: %v\nraw: %s", err, result)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status ok, got %q: %s", tr.Status, tr.Message)
	}
}

func TestTodoAddEmptyContent(t *testing.T) {
	tool := New().ListTools()[0]

	result, err := tool.Run(context.Background(), `{"action":"add","content":""}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("invalid JSON result: %v\nraw: %s", err, result)
	}
	if tr.Status != "error" {
		t.Errorf("expected error for empty content, got status %q", tr.Status)
	}
	if !strings.Contains(tr.Message, "content is required") {
		t.Errorf("expected error about content, got: %s", tr.Message)
	}
}

func TestTodoAddMultiple(t *testing.T) {
	p := New()
	tool := p.ListTools()[0]

	// First add
	r1, _ := tool.Run(context.Background(), `{"action":"add","content":"Task one"}`)
	var tr1 toolutil.ToolResult
	json.Unmarshal([]byte(r1), &tr1)

	data1, _ := tr1.Data.(map[string]any)
	id1, _ := data1["id"].(string)
	if id1 != "T1" {
		t.Errorf("expected first task ID T1, got %q", id1)
	}

	// Second add
	r2, _ := tool.Run(context.Background(), `{"action":"add","content":"Task two"}`)
	var tr2 toolutil.ToolResult
	json.Unmarshal([]byte(r2), &tr2)

	data2, _ := tr2.Data.(map[string]any)
	id2, _ := data2["id"].(string)
	if id2 != "T2" {
		t.Errorf("expected second task ID T2, got %q", id2)
	}
}

func TestTodoUpdate(t *testing.T) {
	p := New()
	tool := p.ListTools()[0]

	// Add a task
	tool.Run(context.Background(), `{"action":"add","content":"Refactor module"}`)

	// Update to in_progress
	result, err := tool.Run(context.Background(), `{"action":"update","id":"T1","status":"in_progress"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("invalid JSON result: %v\nraw: %s", err, result)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status ok, got %q: %s", tr.Status, tr.Message)
	}

	data, _ := tr.Data.(map[string]any)
	if status, _ := data["status"].(string); status != "in_progress" {
		t.Errorf("expected status in_progress, got %q", status)
	}
}

func TestTodoUpdateInvalidStatus(t *testing.T) {
	p := New()
	tool := p.ListTools()[0]

	tool.Run(context.Background(), `{"action":"add","content":"Some task"}`)

	result, _ := tool.Run(context.Background(), `{"action":"update","id":"T1","status":"invalid"}`)
	var tr toolutil.ToolResult
	json.Unmarshal([]byte(result), &tr)
	if tr.Status != "error" {
		t.Errorf("expected error for invalid status, got %q", tr.Status)
	}
}

func TestTodoUpdateNonExistent(t *testing.T) {
	tool := New().ListTools()[0]

	result, _ := tool.Run(context.Background(), `{"action":"update","id":"T999","status":"completed"}`)
	var tr toolutil.ToolResult
	json.Unmarshal([]byte(result), &tr)
	if tr.Status != "error" {
		t.Errorf("expected error for non-existent task, got %q", tr.Status)
	}
}

func TestTodoUpdateMissingID(t *testing.T) {
	tool := New().ListTools()[0]

	result, _ := tool.Run(context.Background(), `{"action":"update","status":"completed"}`)
	var tr toolutil.ToolResult
	json.Unmarshal([]byte(result), &tr)
	if tr.Status != "error" {
		t.Errorf("expected error for missing id, got %q", tr.Status)
	}
}

func TestTodoList(t *testing.T) {
	p := New()
	tool := p.ListTools()[0]

	// List empty
	result, err := tool.Run(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("invalid JSON result: %v\nraw: %s", err, result)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status ok, got %q", tr.Status)
	}
	data, _ := tr.Data.(map[string]any)
	total, _ := data["total"].(float64)
	if total != 0 {
		t.Errorf("expected total 0 for empty list, got %v", total)
	}
}

func TestTodoListWithTasks(t *testing.T) {
	p := New()
	tool := p.ListTools()[0]

	tool.Run(context.Background(), `{"action":"add","content":"Task A"}`)
	tool.Run(context.Background(), `{"action":"add","content":"Task B"}`)
	tool.Run(context.Background(), `{"action":"add","content":"Task C"}`)
	tool.Run(context.Background(), `{"action":"update","id":"T1","status":"completed"}`)
	tool.Run(context.Background(), `{"action":"update","id":"T2","status":"in_progress"}`)

	result, _ := tool.Run(context.Background(), `{"action":"list"}`)
	var tr toolutil.ToolResult
	json.Unmarshal([]byte(result), &tr)

	if tr.Status != "ok" {
		t.Errorf("expected status ok, got %q", tr.Status)
	}

	data, _ := tr.Data.(map[string]any)
	tasks, ok := data["tasks"].([]any)
	if !ok {
		t.Fatal("list result should contain tasks array")
	}
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}

	total, _ := data["total"].(float64)
	if total != 3 {
		t.Errorf("expected total 3, got %v", total)
	}

	// Message should contain markers for all three statuses
	if !strings.Contains(tr.Message, "[x]") {
		t.Error("list message should contain completed marker [x]")
	}
	if !strings.Contains(tr.Message, "[>]") {
		t.Error("list message should contain in_progress marker [>]")
	}
	if !strings.Contains(tr.Message, "[ ]") {
		t.Error("list message should contain pending marker [ ]")
	}
}

func TestTodoUnknownAction(t *testing.T) {
	tool := New().ListTools()[0]

	_, err := tool.Run(context.Background(), `{"action":"unknown"}`)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected error about unknown action, got: %v", err)
	}
}

func TestTodoInvalidJSON(t *testing.T) {
	tool := New().ListTools()[0]

	_, err := tool.Run(context.Background(), `not-json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestTodoFullWorkflow(t *testing.T) {
	p := New()
	tool := p.ListTools()[0]

	// 1. Add three tasks
	tool.Run(context.Background(), `{"action":"add","content":"Design API"}`)
	tool.Run(context.Background(), `{"action":"add","content":"Implement backend"}`)
	tool.Run(context.Background(), `{"action":"add","content":"Write tests"}`)

	// 2. Update first task to in_progress
	r, _ := tool.Run(context.Background(), `{"action":"update","id":"T1","status":"in_progress"}`)
	var tr toolutil.ToolResult
	json.Unmarshal([]byte(r), &tr)
	if tr.Status != "ok" {
		t.Errorf("update T1 should succeed: %s", tr.Message)
	}

	// 3. Update first task to completed
	r, _ = tool.Run(context.Background(), `{"action":"update","id":"T1","status":"completed"}`)
	json.Unmarshal([]byte(r), &tr)
	if tr.Status != "ok" {
		t.Errorf("update T1 to completed should succeed: %s", tr.Message)
	}

	// 4. Update second task to in_progress
	r, _ = tool.Run(context.Background(), `{"action":"update","id":"T2","status":"in_progress"}`)
	json.Unmarshal([]byte(r), &tr)
	if tr.Status != "ok" {
		t.Errorf("update T2 should succeed: %s", tr.Message)
	}

	// 5. List and verify
	r, _ = tool.Run(context.Background(), `{"action":"list"}`)
	json.Unmarshal([]byte(r), &tr)
	if tr.Status != "ok" {
		t.Errorf("list should succeed: %s", tr.Message)
	}

	data, _ := tr.Data.(map[string]any)
	tasks, _ := data["tasks"].([]any)
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}

	// Verify IDs are in order
	for i, raw := range tasks {
		task, _ := raw.(map[string]any)
		expectedID := "T" + string(rune('1'+i))
		if id, _ := task["id"].(string); id != expectedID {
			t.Errorf("task %d: expected id %s, got %s", i, expectedID, id)
		}
	}

	// Total should be 3
	total, _ := data["total"].(float64)
	if total != 3 {
		t.Errorf("expected total 3, got %v", total)
	}
}

func TestTodoToolMeta(t *testing.T) {
	p := New()
	tool := p.ListTools()[0]
	meta := tool.ToolMeta()
	if meta.Source != "builtin" {
		t.Errorf("expected source 'builtin', got %q", meta.Source)
	}
}

func TestTodoReturnsValidJSON(t *testing.T) {
	tool := New().ListTools()[0]
	actions := []string{
		`{"action":"add","content":"Test task"}`,
		`{"action":"list"}`,
	}
	for _, args := range actions {
		result, err := tool.Run(context.Background(), args)
		if err != nil {
			// add and list should not return raw errors
			t.Errorf("unexpected error for %s: %v", args, err)
			continue
		}
		var tr toolutil.ToolResult
		if err := json.Unmarshal([]byte(result), &tr); err != nil {
			t.Errorf("invalid JSON for %s: %v\nraw: %s", args, err, result)
		}
	}
}

func TestTodoUpdateTaskToAllStatuses(t *testing.T) {
	p := New()
	tool := p.ListTools()[0]

	tool.Run(context.Background(), `{"action":"add","content":"Versatile task"}`)

	for _, s := range []string{"pending", "in_progress", "completed"} {
		result, _ := tool.Run(context.Background(), `{"action":"update","id":"T1","status":"`+s+`"}`)
		var tr toolutil.ToolResult
		json.Unmarshal([]byte(result), &tr)
		if tr.Status != "ok" {
			t.Errorf("update to %q should succeed, got: %s", s, tr.Message)
		}
	}
}
