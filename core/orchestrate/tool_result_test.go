package orchestrate

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// multimodalTool implements kernel.Tool and the optional kernel.ToolWithResult
// capability: its result carries an image part alongside the text.
type multimodalTool struct{ name string }

func (t *multimodalTool) Name() string              { return t.name }
func (t *multimodalTool) Description() string       { return "multimodal" }
func (t *multimodalTool) Schema() map[string]any    { return map[string]any{"type": "object"} }
func (t *multimodalTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Kind: kernel.ToolKindFunction} }
func (t *multimodalTool) Run(ctx context.Context, args string) (string, error) {
	return "[screenshot:shot_1]", nil
}
func (t *multimodalTool) RunWithResult(ctx context.Context, args string) (string, []types.ContentPart, error) {
	return "[screenshot:shot_1]", []types.ContentPart{
		types.NewImageDataPart("iVBORw0KGgo=", "image/png", "auto"),
	}, nil
}

// TestExecToolsConcurrentResults_ToolWithResult verifies that a tool
// implementing ToolWithResult carries its multimodal parts onto the tool
// message, while a text-only tool stays unchanged (zero-value = old behavior).
func TestExecToolsConcurrentResults_ToolWithResult(t *testing.T) {
	registry := &mockRegistry{tools: map[string]kernel.Tool{
		"shot": &multimodalTool{name: "shot"},
		"txt":  &mockTool{name: "txt", fn: func(ctx context.Context, args string) (string, error) { return "plain", nil }},
	}}

	results := ExecToolsConcurrentResults(context.Background(), []*types.ToolCall{
		{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "shot", Arguments: "{}"}},
		{ID: "c2", Type: "function", Function: types.ToolCallFunction{Name: "txt", Arguments: "{}"}},
	}, registry)

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	shot := results[0].Message
	if shot.Role != types.RoleTool {
		t.Fatalf("shot role = %s, want tool", shot.Role)
	}
	if got := len(shot.ContentParts); got != 1 {
		t.Fatalf("shot parts = %d, want 1", got)
	}
	if shot.ContentParts[0].Type != types.ContentTypeImageData {
		t.Fatalf("shot part type = %s, want image_data", shot.ContentParts[0].Type)
	}
	if shot.Content != "[screenshot:shot_1]" {
		t.Fatalf("shot content = %q, want the text marker", shot.Content)
	}

	plain := results[1].Message
	if len(plain.ContentParts) != 0 {
		t.Fatalf("plain parts = %d, want 0 (text-only path unchanged)", len(plain.ContentParts))
	}
	if plain.Content != "plain" {
		t.Fatalf("plain content = %q, want %q", plain.Content, "plain")
	}
}
