package runtime_test

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// screenshotTool implements kernel.Tool + kernel.ToolWithResult, returning an
// image part alongside its text marker.
type screenshotTool struct{}

func (s *screenshotTool) Name() string              { return "screenshot" }
func (s *screenshotTool) Description() string       { return "capture a screenshot" }
func (s *screenshotTool) Schema() map[string]any    { return map[string]any{"type": "object"} }
func (s *screenshotTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Kind: kernel.ToolKindFunction} }
func (s *screenshotTool) Run(ctx context.Context, args string) (string, error) {
	return "[screenshot:shot_1]", nil
}
func (s *screenshotTool) RunWithResult(ctx context.Context, args string) (string, []types.ContentPart, error) {
	return "[screenshot:shot_1]", []types.ContentPart{
		types.NewImageDataPart("iVBORw0KGgo=", "image/png", "auto"),
	}, nil
}

func newScreenshotRuntime() *runtime.Runtime {
	reg := tool.NewMapToolRegistry([]kernel.Tool{&screenshotTool{}})
	return runtime.NewRuntime(&mockModel{}, reg)
}

func execScreenshot(t *testing.T, rt *runtime.Runtime) *types.Message {
	t.Helper()
	msgs := rt.ExecTools(context.Background(), []*types.ToolCall{
		{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "screenshot", Arguments: "{}"}},
	})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	return msgs[0]
}

// TestOnToolResult_KeepsPartsWhenHookReturnsNewInfo (P1 regression): a hook
// that returns a fresh ToolCallInfo without touching Parts must not wipe the
// tool's own parts.
func TestOnToolResult_KeepsPartsWhenHookReturnsNewInfo(t *testing.T) {
	rt := newScreenshotRuntime()
	rt.OnToolResult(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		// Return a NEW object that only rewrites text, leaving Parts nil.
		return ctx, &kernel.ToolCallInfo{Result: "sanitized:" + info.Result}, nil
	})

	msg := execScreenshot(t, rt)
	if len(msg.ContentParts) != 1 || msg.ContentParts[0].Type != types.ContentTypeImageData {
		t.Fatalf("parts were lost: %+v", msg.ContentParts)
	}
	if msg.Content != "sanitized:[screenshot:shot_1]" {
		t.Fatalf("content = %q, want sanitized text", msg.Content)
	}
}

// TestOnToolResult_ReplacesPartsWhenHookSetsThem: a hook may attach its own
// parts in the same step.
func TestOnToolResult_ReplacesPartsWhenHookSetsThem(t *testing.T) {
	rt := newScreenshotRuntime()
	rt.OnToolResult(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		info.Parts = []types.ContentPart{types.NewImageDataPart("AAAA", "image/jpeg", "high")}
		return ctx, info, nil
	})

	msg := execScreenshot(t, rt)
	if len(msg.ContentParts) != 1 {
		t.Fatalf("parts = %d, want 1", len(msg.ContentParts))
	}
	if got := msg.ContentParts[0].ImageData.MIMEType; got != "image/jpeg" {
		t.Fatalf("part mime = %q, want image/jpeg (hook-replaced)", got)
	}
}
