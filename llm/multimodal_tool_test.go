package llm

import (
	"testing"

	"github.com/go-gocel/gocel/core/types"
)

// TestSplitToolParts verifies a tool result's content parts split into the
// textual string and the non-text media parts.
func TestSplitToolParts(t *testing.T) {
	msg := &types.Message{
		Role:    types.RoleTool,
		Content: "legacy text",
		ContentParts: []types.ContentPart{
			types.NewTextPart("[screenshot:shot_1]"),
			types.NewImageDataPart("iVBORw0KGgo=", "image/png", "auto"),
		},
	}
	text, media := splitToolParts(msg)
	if text != "[screenshot:shot_1]" {
		t.Fatalf("text = %q, want %q", text, "[screenshot:shot_1]")
	}
	if len(media) != 1 || media[0].Type != types.ContentTypeImageData {
		t.Fatalf("media = %+v, want one image_data part", media)
	}
}

// TestMessagesToOpenAI_SplitsToolMedia verifies a tool message carrying media
// serializes into a textual tool message + a trailing user media message
// (OpenAI only accepts media on `user` messages).
func TestMessagesToOpenAI_SplitsToolMedia(t *testing.T) {
	msgs := []*types.Message{
		{
			Role:       types.RoleTool,
			Content:    "[screenshot:shot_1]",
			ToolCallID: "call_1",
			ToolName:   "screenshot",
			ContentParts: []types.ContentPart{
				types.NewTextPart("[screenshot:shot_1]"),
				types.NewImageDataPart("iVBORw0KGgo=", "image/png", "auto"),
			},
		},
	}

	out := messagesToOpenAI(msgs)
	if len(out) != 2 {
		t.Fatalf("got %d messages, want 2 (tool text + user media)", len(out))
	}

	toolMsg := out[0]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" {
		t.Fatalf("tool msg = %+v", toolMsg)
	}
	if toolMsg.Content != "[screenshot:shot_1]" {
		t.Fatalf("tool content = %v, want %q", toolMsg.Content, "[screenshot:shot_1]")
	}

	userMsg := out[1]
	if userMsg.Role != "user" {
		t.Fatalf("media msg role = %s, want user", userMsg.Role)
	}
	parts, ok := userMsg.Content.([]openaiContentPart)
	if !ok || len(parts) != 1 || parts[0].Type != "image_url" {
		t.Fatalf("media content = %#v, want one image_url part", userMsg.Content)
	}
}

// TestMessagesToOpenAI_ToolTextOnlyParts (P2 regression): a tool message with
// only text parts must serialize to a string content, never a content array
// (OpenAI requires tool message content to be a string).
func TestMessagesToOpenAI_ToolTextOnlyParts(t *testing.T) {
	msgs := []*types.Message{
		{
			Role:       types.RoleTool,
			Content:    "[screenshot:shot_1]",
			ToolCallID: "call_1",
			ToolName:   "screenshot",
			ContentParts: []types.ContentPart{
				types.NewTextPart("[screenshot:shot_1]"),
			},
		},
	}
	out := messagesToOpenAI(msgs)
	if len(out) != 1 {
		t.Fatalf("got %d messages, want 1 (text-only tool message stays single)", len(out))
	}
	if _, isArray := out[0].Content.([]openaiContentPart); isArray {
		t.Fatalf("tool content must be a string, got a content array")
	}
	if out[0].Content != "[screenshot:shot_1]" {
		t.Fatalf("tool content = %v, want %q", out[0].Content, "[screenshot:shot_1]")
	}
}
