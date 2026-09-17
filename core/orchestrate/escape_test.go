package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

type boomTool struct{}

func (boomTool) Name() string        { return "boom" }
func (boomTool) Description() string { return "" }
func (boomTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (boomTool) Run(context.Context, string) (string, error) {
	return "", errors.New("boom \"quoted\" and \nnewline")
}
func (boomTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{} }

// panicTool panics inside Run.
type panicTool struct{}

func (panicTool) Name() string        { return "panic" }
func (panicTool) Description() string { return "" }
func (panicTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (panicTool) Run(context.Context, string) (string, error) {
	panic("tool bug")
}
func (panicTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{} }

type mapReg struct{ tools map[string]kernel.Tool }

func (m *mapReg) List(context.Context) []kernel.Tool {
	out := make([]kernel.Tool, 0, len(m.tools))
	for _, t := range m.tools {
		out = append(out, t)
	}
	return out
}
func (m *mapReg) Get(_ context.Context, name string) kernel.Tool { return m.tools[name] }
func (m *mapReg) Add(_ context.Context, t kernel.Tool) error     { m.tools[t.Name()] = t; return nil }
func (m *mapReg) Remove(_ context.Context, name string) error    { delete(m.tools, name); return nil }

// TestExecToolsConcurrentResults_ErrorPayloadValidJSON: tool error text is
// embedded in {"error": ...} payloads — quotes/newlines in the error must
// not produce invalid JSON fed back to the model (C6).
func TestExecToolsConcurrentResults_ErrorPayloadValidJSON(t *testing.T) {
	reg := &mapReg{tools: map[string]kernel.Tool{"boom": boomTool{}}}
	results := ExecToolsConcurrentResults(context.Background(),
		[]*types.ToolCall{{ID: "1", Function: types.ToolCallFunction{Name: "boom", Arguments: "{}"}}},
		reg)
	if len(results) != 1 || results[0].Message == nil {
		t.Fatalf("expected 1 result message")
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(results[0].Message.Content), &v); err != nil {
		t.Fatalf("error payload is not valid JSON: %v\ncontent: %s", err, results[0].Message.Content)
	}
	if !strings.Contains(v["error"], "quoted") {
		t.Fatalf("error payload missing text: %v", v)
	}
}

// TestExecToolsConcurrentResults_NilToolCallNoPanic: a nil element must not
// crash the batch (public API input).
func TestExecToolsConcurrentResults_NilToolCallNoPanic(t *testing.T) {
	results := ExecToolsConcurrentResults(context.Background(), []*types.ToolCall{nil}, &mapReg{tools: map[string]kernel.Tool{}})
	if len(results) != 1 || results[0].Message == nil {
		t.Fatalf("expected 1 error result for nil tool call")
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(results[0].Message.Content), &v); err != nil {
		t.Fatalf("nil-call payload is not valid JSON: %v", err)
	}
}

// TestExecToolsConcurrentResults_NilRegistryNoPanic: a nil registry must
// not panic — each call degrades to an error payload.
func TestExecToolsConcurrentResults_NilRegistryNoPanic(t *testing.T) {
	results := ExecToolsConcurrentResults(context.Background(),
		[]*types.ToolCall{{ID: "1", Function: types.ToolCallFunction{Name: "boom", Arguments: "{}"}}},
		nil)
	if len(results) != 1 || results[0].Message == nil {
		t.Fatalf("expected 1 error result for nil registry")
	}
	if results[0].Err == nil {
		t.Fatalf("nil registry must surface an error")
	}
}

// TestExecToolsConcurrentResults_PanickingToolBecomesError: a tool that
// panics must degrade to an error payload message, never crash the host
// (C3).
func TestExecToolsConcurrentResults_PanickingToolBecomesError(t *testing.T) {
	reg := &mapReg{tools: map[string]kernel.Tool{"panic": panicTool{}}}
	results := ExecToolsConcurrentResults(context.Background(),
		[]*types.ToolCall{{ID: "1", Function: types.ToolCallFunction{Name: "panic", Arguments: "{}"}}},
		reg)
	if len(results) != 1 || results[0].Message == nil {
		t.Fatalf("expected 1 result message")
	}
	if results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "panicked") {
		t.Fatalf("err = %v, want a panic error", results[0].Err)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(results[0].Message.Content), &v); err != nil {
		t.Fatalf("panic payload is not valid JSON: %v", err)
	}
}
