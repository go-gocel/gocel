package types_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

func TestRoleConstants(t *testing.T) {
	t.Run("values", func(t *testing.T) {
		if types.RoleSystem != "system" {
			t.Fatalf("RoleSystem = %q, want %q", types.RoleSystem, "system")
		}
		if types.RoleUser != "user" {
			t.Fatalf("RoleUser = %q, want %q", types.RoleUser, "user")
		}
		if types.RoleAssistant != "assistant" {
			t.Fatalf("RoleAssistant = %q, want %q", types.RoleAssistant, "assistant")
		}
		if types.RoleTool != "tool" {
			t.Fatalf("RoleTool = %q, want %q", types.RoleTool, "tool")
		}
	})
}

func TestNewSystemMessage(t *testing.T) {
	m := types.NewSystemMessage("be helpful")
	if m.Role != types.RoleSystem {
		t.Fatalf("Role = %q, want %q", m.Role, types.RoleSystem)
	}
	if m.Content != "be helpful" {
		t.Fatalf("Content = %q, want %q", m.Content, "be helpful")
	}
}

func TestNewUserMessage(t *testing.T) {
	m := types.NewUserMessage("hello")
	if m.Role != types.RoleUser {
		t.Fatalf("Role = %q, want %q", m.Role, types.RoleUser)
	}
	if m.Content != "hello" {
		t.Fatalf("Content = %q, want %q", m.Content, "hello")
	}
}

func TestNewAssistantMessage(t *testing.T) {
	m := types.NewAssistantMessage("hi there")
	if m.Role != types.RoleAssistant {
		t.Fatalf("Role = %q, want %q", m.Role, types.RoleAssistant)
	}
	if m.Content != "hi there" {
		t.Fatalf("Content = %q, want %q", m.Content, "hi there")
	}
}

func TestNewToolMessage(t *testing.T) {
	m := types.NewToolMessage("result", "call_1", "get_weather")
	if m.Role != types.RoleTool {
		t.Fatalf("Role = %q, want %q", m.Role, types.RoleTool)
	}
	if m.Content != "result" {
		t.Fatalf("Content = %q, want %q", m.Content, "result")
	}
	if m.ToolCallID != "call_1" {
		t.Fatalf("ToolCallID = %q, want %q", m.ToolCallID, "call_1")
	}
	if m.ToolName != "get_weather" {
		t.Fatalf("ToolName = %q, want %q", m.ToolName, "get_weather")
	}
}

func TestMessageFields(t *testing.T) {
	idx := 0
	m := &types.Message{
		Role:             types.RoleAssistant,
		Content:          "content",
		ReasoningContent: "thinking",
		ToolCalls: []types.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      "get_weather",
				Arguments: `{"city":"NYC"}`,
			},
			Index: &idx,
		}},
		ToolCallID: "call_1",
		ToolName:   "get_weather",
		Meta:       map[string]any{"key": "val"},
	}
	if string(m.Role) != "assistant" {
		t.Fatal("bad role")
	}
	if m.Content != "content" {
		t.Fatal("bad content")
	}
	if m.ReasoningContent != "thinking" {
		t.Fatal("bad reasoning_content")
	}
	if len(m.ToolCalls) != 1 {
		t.Fatal("bad tool_calls")
	}
	if m.ToolCalls[0].ID != "call_1" || m.ToolCalls[0].Type != "function" {
		t.Fatal("bad toolcall")
	}
	if m.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatal("bad function name")
	}
	if m.ToolCalls[0].Function.Arguments != `{"city":"NYC"}` {
		t.Fatal("bad function args")
	}
	if *m.ToolCalls[0].Index != 0 {
		t.Fatal("bad index")
	}
	if m.Meta["key"] != "val" {
		t.Fatal("bad meta")
	}
}

func TestTokenUsage(t *testing.T) {
	u := types.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}
	if u.PromptTokens != 10 || u.CompletionTokens != 20 || u.TotalTokens != 30 {
		t.Fatal("TokenUsage fields mismatch")
	}
}

func TestSessionID(t *testing.T) {
	id1 := types.SessionID()
	id2 := types.SessionID()
	if !strings.HasPrefix(id1, "sess_") {
		t.Fatalf("SessionID %q does not start with sess_", id1)
	}
	if !strings.HasPrefix(id2, "sess_") {
		t.Fatalf("SessionID %q does not start with sess_", id2)
	}
	if id1 == id2 {
		t.Fatal("SessionIDs should be unique")
	}
}

func TestCloneMessage(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if types.CloneMessage(nil) != nil {
			t.Fatal("expected nil")
		}
	})

	t.Run("deep copy", func(t *testing.T) {
		idx := 0
		orig := &types.Message{
			Role:             types.RoleAssistant,
			Content:          "hello",
			ReasoningContent: "thinking",
			ToolCalls: []types.ToolCall{{
				ID: "tc1",
				Function: types.ToolCallFunction{
					Name:      "fn",
					Arguments: "{}",
				},
				Index: &idx,
			}},
			ToolCallID: "tc1",
			ToolName:   "fn",
			Meta:       map[string]any{"k": "v"},
		}
		clone := types.CloneMessage(orig)

		if clone.Role != orig.Role || clone.Content != orig.Content {
			t.Fatal("fields mismatch")
		}
		orig.Content = "modified"
		if clone.Content == orig.Content {
			t.Fatal("clone should be independent")
		}
		orig.Meta["k"] = "v2"
		if clone.Meta["k"] != "v" {
			t.Fatal("meta should be independent")
		}
		if &clone.ToolCalls[0] == &orig.ToolCalls[0] {
			t.Fatal("ToolCalls should be independent")
		}
	})
}

func TestCloneMessages(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if types.CloneMessages(nil) != nil {
			t.Fatal("expected nil")
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		result := types.CloneMessages([]*types.Message{})
		if len(result) != 0 {
			t.Fatal("expected empty")
		}
	})

	t.Run("deep copy slice", func(t *testing.T) {
		msgs := []*types.Message{
			types.NewUserMessage("hi"),
			types.NewAssistantMessage("bye"),
		}
		clone := types.CloneMessages(msgs)
		if len(clone) != 2 {
			t.Fatal("length mismatch")
		}
		if clone[0].Content != "hi" || clone[1].Content != "bye" {
			t.Fatal("content mismatch")
		}
		msgs[0].Content = "changed"
		if clone[0].Content == "changed" {
			t.Fatal("clone should be independent")
		}
	})
}

func TestEstimateTokens(t *testing.T) {
	t.Run("nil slice", func(t *testing.T) {
		if types.EstimateTokens(nil) != 0 {
			t.Fatal("expected 0")
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		if types.EstimateTokens([]*types.Message{}) != 0 {
			t.Fatal("expected 0")
		}
	})

	t.Run("text messages", func(t *testing.T) {
		msgs := []*types.Message{
			types.NewSystemMessage("you are helpful"),
			types.NewUserMessage("hello world"),
		}
		tokens := types.EstimateTokens(msgs)
		if tokens <= 0 {
			t.Fatal("expected positive token count")
		}
	})

	t.Run("nil message in slice", func(t *testing.T) {
		msgs := []*types.Message{nil, types.NewUserMessage("test")}
		tokens := types.EstimateTokens(msgs)
		if tokens <= 0 {
			t.Fatal("expected positive token count")
		}
	})

	t.Run("with tool calls", func(t *testing.T) {
		msgs := []*types.Message{{
			Role: types.RoleAssistant,
			ToolCalls: []types.ToolCall{{
				Function: types.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"NYC"}`},
			}},
		}}
		tokens := types.EstimateTokens(msgs)
		if tokens <= 0 {
			t.Fatal("expected positive token count")
		}
	})

	t.Run("with reasoning content", func(t *testing.T) {
		msgs := []*types.Message{{
			Role:             types.RoleAssistant,
			Content:          "answer",
			ReasoningContent: "deep thinking here",
		}}
		tokens := types.EstimateTokens(msgs)
		if tokens <= 0 {
			t.Fatal("expected positive token count")
		}
	})
}

func TestEventTypeConstants(t *testing.T) {
	if types.EventToken != 0 {
		t.Fatal("EventToken should be 0")
	}
	if types.EventToolCall != 1 {
		t.Fatal("EventToolCall should be 1")
	}
	if types.EventToolResult != 2 {
		t.Fatal("EventToolResult should be 2")
	}
	if types.EventFinish != 3 {
		t.Fatal("EventFinish should be 3")
	}
	if types.EventError != 4 {
		t.Fatal("EventError should be 4")
	}
	if types.EventInterrupt != 5 {
		t.Fatal("EventInterrupt should be 5")
	}
	if types.EventParseResult != 6 {
		t.Fatal("EventParseResult should be 6")
	}
}

func TestEventTypeString(t *testing.T) {
	cases := []struct {
		et       types.EventType
		expected string
	}{
		{types.EventToken, "token"},
		{types.EventToolCall, "tool_call"},
		{types.EventToolResult, "tool_result"},
		{types.EventFinish, "finish"},
		{types.EventError, "error"},
		{types.EventInterrupt, "interrupt"},
		{types.EventParseResult, "parse_result"},
		{types.EventType(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.et.String(); got != c.expected {
			t.Fatalf("EventType(%d).String() = %q, want %q", c.et, got, c.expected)
		}
	}
}

func TestFinishEvent(t *testing.T) {
	e := types.FinishEvent()
	if e.Type != types.EventFinish {
		t.Fatal("expected EventFinish")
	}
}

func TestErrorEvent(t *testing.T) {
	err := errors.New("test error")
	e := types.ErrorEvent(err)
	if e.Type != types.EventError {
		t.Fatal("expected EventError")
	}
	if e.Error != err {
		t.Fatal("error mismatch")
	}
}

func TestTokenEvent(t *testing.T) {
	e := types.TokenEvent("hello")
	if e.Type != types.EventToken {
		t.Fatal("expected EventToken")
	}
	if e.Content != "hello" {
		t.Fatal("content mismatch")
	}
}

func TestToolCallEvent(t *testing.T) {
	e := types.ToolCallEvent("get_weather", `{"city":"NYC"}`, "call_abc")
	if e.Type != types.EventToolCall {
		t.Fatal("expected EventToolCall")
	}
	if e.ToolName != "get_weather" {
		t.Fatal("tool name mismatch")
	}
	if e.ToolArgs != `{"city":"NYC"}` {
		t.Fatal("tool args mismatch")
	}
	if e.ToolCallID != "call_abc" {
		t.Fatal("tool call id mismatch")
	}
}

func TestToolResultEvent(t *testing.T) {
	e := types.ToolResultEvent("get_weather", "sunny", "call_abc")
	if e.Type != types.EventToolResult {
		t.Fatal("expected EventToolResult")
	}
	if e.ToolName != "get_weather" {
		t.Fatal("tool name mismatch")
	}
	if e.Content != "sunny" {
		t.Fatal("content mismatch")
	}
	if e.ToolCallID != "call_abc" {
		t.Fatal("tool call id mismatch")
	}
}

func TestParseResultEvent(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		val := 42
		e := types.ParseResultEvent(`{"value":42}`, val, nil)
		if e.Type != types.EventParseResult {
			t.Fatal("expected EventParseResult")
		}
		if e.Error != nil {
			t.Fatal("expected no error")
		}
		if e.Content != `{"value":42}` {
			t.Fatal("content mismatch")
		}
		if e.ParseResult == nil {
			t.Fatal("expected ParseResult")
		}
		if e.ParseResult.Value != 42 {
			t.Fatal("value mismatch")
		}
	})

	t.Run("error", func(t *testing.T) {
		err := errors.New("parse failed")
		e := types.ParseResultEvent("raw", nil, err)
		if e.Type != types.EventParseResult {
			t.Fatal("expected EventParseResult")
		}
		if e.Error != err {
			t.Fatal("error mismatch")
		}
		if e.ParseResult.Error != err {
			t.Fatal("parse result error mismatch")
		}
	})
}

func TestParseResultStruct(t *testing.T) {
	pr := &types.ParseResult{Raw: "raw", Value: "val", Error: nil}
	if pr.Raw != "raw" || pr.Value != "val" || pr.Error != nil {
		t.Fatal("ParseResult fields mismatch")
	}
}

func TestAgentInput(t *testing.T) {
	in := &types.AgentInput{
		Messages:        []*types.Message{types.NewUserMessage("hi")},
		SystemPrompt:    "be helpful",
		EnableStreaming: true,
		MaxSteps:        10,
		Meta:            map[string]any{"k": "v"},
		InterruptInput:  make(chan string),
	}
	if len(in.Messages) != 1 {
		t.Fatal("messages mismatch")
	}
	if in.SystemPrompt != "be helpful" {
		t.Fatal("system prompt mismatch")
	}
	if !in.EnableStreaming {
		t.Fatal("expected streaming enabled")
	}
	if in.MaxSteps != 10 {
		t.Fatal("max steps mismatch")
	}
}

func TestTrimReport(t *testing.T) {
	tr := &types.TrimReport{
		Truncated:    true,
		OriginalSize: 1000,
		FinalSize:    500,
		Strategy:     "compact",
	}
	if !tr.Truncated || tr.OriginalSize != 1000 || tr.FinalSize != 500 || tr.Strategy != "compact" {
		t.Fatal("TrimReport fields mismatch")
	}
}

func TestContentTypeConstants(t *testing.T) {
	if types.ContentTypeText != "text" {
		t.Fatal("ContentTypeText mismatch")
	}
	if types.ContentTypeImageURL != "image_url" {
		t.Fatal("ContentTypeImageURL mismatch")
	}
	if types.ContentTypeImageData != "image_data" {
		t.Fatal("ContentTypeImageData mismatch")
	}
	if types.ContentTypeAudioData != "audio_data" {
		t.Fatal("ContentTypeAudioData mismatch")
	}
	if types.ContentTypeVideoData != "video_data" {
		t.Fatal("ContentTypeVideoData mismatch")
	}
	if types.ContentTypeFileData != "file_data" {
		t.Fatal("ContentTypeFileData mismatch")
	}
}

func TestNewTextPart(t *testing.T) {
	p := types.NewTextPart("hello")
	if p.Type != types.ContentTypeText || p.Text != "hello" {
		t.Fatal("TextPart mismatch")
	}
}

func TestNewImageURLPart(t *testing.T) {
	p := types.NewImageURLPart("https://example.com/img.png")
	if p.Type != types.ContentTypeImageURL || p.ImageURL != "https://example.com/img.png" {
		t.Fatal("ImageURLPart mismatch")
	}
}

func TestNewImageDataPart(t *testing.T) {
	p := types.NewImageDataPart("base64data", "image/png", "high")
	if p.Type != types.ContentTypeImageData {
		t.Fatal("type mismatch")
	}
	if p.ImageData == nil {
		t.Fatal("expected ImageData")
	}
	if p.ImageData.Data != "base64data" || p.ImageData.MIMEType != "image/png" || p.ImageData.Detail != "high" {
		t.Fatal("ImageData fields mismatch")
	}
}

func TestNewAudioDataPart(t *testing.T) {
	p := types.NewAudioDataPart("audiodata", "audio/mpeg")
	if p.Type != types.ContentTypeAudioData {
		t.Fatal("type mismatch")
	}
	if p.AudioData == nil {
		t.Fatal("expected AudioData")
	}
	if p.AudioData.Data != "audiodata" || p.AudioData.MIMEType != "audio/mpeg" {
		t.Fatal("AudioData fields mismatch")
	}
}

func TestNewVideoDataPart(t *testing.T) {
	p := types.NewVideoDataPart("videodata", "video/mp4")
	if p.Type != types.ContentTypeVideoData {
		t.Fatal("type mismatch")
	}
	if p.VideoData == nil {
		t.Fatal("expected VideoData")
	}
	if p.VideoData.Data != "videodata" || p.VideoData.MIMEType != "video/mp4" {
		t.Fatal("VideoData fields mismatch")
	}
}

func TestNewFileDataPart(t *testing.T) {
	p := types.NewFileDataPart("filedata", "application/pdf", "doc.pdf")
	if p.Type != types.ContentTypeFileData {
		t.Fatal("type mismatch")
	}
	if p.FileData == nil {
		t.Fatal("expected FileData")
	}
	if p.FileData.Data != "filedata" || p.FileData.MIMEType != "application/pdf" || p.FileData.Name != "doc.pdf" {
		t.Fatal("FileData fields mismatch")
	}
}

func TestNewUserMultimodalMessage(t *testing.T) {
	p1 := types.NewTextPart("describe this")
	p2 := types.NewImageURLPart("https://example.com/img.png")
	m := types.NewUserMultimodalMessage(p1, p2)
	if m.Role != types.RoleUser {
		t.Fatal("expected RoleUser")
	}
	if len(m.ContentParts) != 2 {
		t.Fatal("expected 2 content parts")
	}
	if m.ContentParts[0].Type != types.ContentTypeText {
		t.Fatal("first part should be text")
	}
}

func TestNewSystemMultimodalMessage(t *testing.T) {
	p := types.NewTextPart("system instruction")
	m := types.NewSystemMultimodalMessage(p)
	if m.Role != types.RoleSystem {
		t.Fatal("expected RoleSystem")
	}
	if len(m.ContentParts) != 1 {
		t.Fatal("expected 1 content part")
	}
}

func TestHasMultimodalContent(t *testing.T) {
	t.Run("nil message", func(t *testing.T) {
		if (*types.Message)(nil).HasMultimodalContent() {
			t.Fatal("expected false for nil")
		}
	})

	t.Run("no parts", func(t *testing.T) {
		m := types.NewUserMessage("text")
		if m.HasMultimodalContent() {
			t.Fatal("expected false")
		}
	})

	t.Run("text only parts", func(t *testing.T) {
		m := types.NewUserMultimodalMessage(types.NewTextPart("hello"))
		if m.HasMultimodalContent() {
			t.Fatal("expected false for text-only parts")
		}
	})

	t.Run("non-text parts", func(t *testing.T) {
		m := types.NewUserMultimodalMessage(
			types.NewTextPart("desc"),
			types.NewImageURLPart("url"),
		)
		if !m.HasMultimodalContent() {
			t.Fatal("expected true")
		}
	})
}

func TestIsMultimodal(t *testing.T) {
	t.Run("nil message", func(t *testing.T) {
		if (*types.Message)(nil).IsMultimodal() {
			t.Fatal("expected false for nil")
		}
	})

	t.Run("no parts", func(t *testing.T) {
		m := types.NewUserMessage("text")
		if m.IsMultimodal() {
			t.Fatal("expected false")
		}
	})

	t.Run("has parts", func(t *testing.T) {
		m := types.NewUserMultimodalMessage(types.NewTextPart("hello"))
		if !m.IsMultimodal() {
			t.Fatal("expected true")
		}
	})
}

func TestHITLModeConstants(t *testing.T) {
	if types.HITLModeConfirm != "confirm" {
		t.Fatal("HITLModeConfirm mismatch")
	}
	if types.HITLModeReview != "review" {
		t.Fatal("HITLModeReview mismatch")
	}
	if types.HITLModeSelect != "select" {
		t.Fatal("HITLModeSelect mismatch")
	}
}

func TestHITLStatusConstants(t *testing.T) {
	if types.HITLStatusPending != "pending" {
		t.Fatal("HITLStatusPending mismatch")
	}
	if types.HITLStatusApproved != "approved" {
		t.Fatal("HITLStatusApproved mismatch")
	}
	if types.HITLStatusRejected != "rejected" {
		t.Fatal("HITLStatusRejected mismatch")
	}
	if types.HITLStatusModified != "modified" {
		t.Fatal("HITLStatusModified mismatch")
	}
	if types.HITLStatusTimedOut != "timed_out" {
		t.Fatal("HITLStatusTimedOut mismatch")
	}
}

func TestNewHITLInfo(t *testing.T) {
	info := types.NewHITLInfo(types.HITLModeConfirm, "get_weather", `{"city":"NYC"}`)
	if info.Mode != types.HITLModeConfirm {
		t.Fatal("mode mismatch")
	}
	if info.Status != types.HITLStatusPending {
		t.Fatal("expected pending status")
	}
	if info.ToolName != "get_weather" {
		t.Fatal("tool name mismatch")
	}
	if info.ToolArgs != `{"city":"NYC"}` {
		t.Fatal("tool args mismatch")
	}
	if info.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
}

func TestHITLInfoFullFields(t *testing.T) {
	now := time.Now()
	info := &types.HITLInfo{
		Mode:         types.HITLModeSelect,
		Status:       types.HITLStatusPending,
		ToolName:     "search",
		ToolArgs:     `{"q":"test"}`,
		AgentName:    "agent1",
		Branch:       "main",
		InvocationID: "inv_1",
		SessionID:    "sess_1",
		Timestamp:    now,
		Options:      []string{"a", "b"},
		Prompt:       "choose one",
	}
	if info.Mode != types.HITLModeSelect || info.Status != types.HITLStatusPending {
		t.Fatal("basic fields mismatch")
	}
	if info.AgentName != "agent1" || info.Branch != "main" {
		t.Fatal("agent/branch mismatch")
	}
	if info.InvocationID != "inv_1" || info.SessionID != "sess_1" {
		t.Fatal("id fields mismatch")
	}
	if !info.Timestamp.Equal(now) {
		t.Fatal("timestamp mismatch")
	}
	if len(info.Options) != 2 || info.Prompt != "choose one" {
		t.Fatal("options/prompt mismatch")
	}
}

func TestHITLDecision(t *testing.T) {
	t.Run("approved", func(t *testing.T) {
		d := &types.HITLDecision{Approved: true}
		if !d.IsFinal() {
			t.Fatal("approved should be final")
		}
	})

	t.Run("skipped", func(t *testing.T) {
		d := &types.HITLDecision{Skip: true}
		if !d.IsFinal() {
			t.Fatal("skipped should be final")
		}
	})

	t.Run("not final", func(t *testing.T) {
		d := &types.HITLDecision{}
		if d.IsFinal() {
			t.Fatal("empty decision should not be final")
		}
	})

	t.Run("modified args", func(t *testing.T) {
		d := &types.HITLDecision{
			Approved:     true,
			ModifiedArgs: `{"city":"LA"}`,
			Feedback:     "use LA instead",
		}
		if !d.IsFinal() {
			t.Fatal("should be final")
		}
		if d.ModifiedArgs != `{"city":"LA"}` {
			t.Fatal("modified args mismatch")
		}
		if d.Feedback != "use LA instead" {
			t.Fatal("feedback mismatch")
		}
	})

	t.Run("selected option", func(t *testing.T) {
		d := &types.HITLDecision{
			Approved:       true,
			SelectedOption: "option_b",
		}
		if d.SelectedOption != "option_b" {
			t.Fatal("selected option mismatch")
		}
	})
}

func TestHITLInfoToInterruptEvent(t *testing.T) {
	info := types.NewHITLInfo(types.HITLModeConfirm, "get_weather", `{"city":"NYC"}`)
	e := info.ToInterruptEvent()
	if e.Type != types.EventInterrupt {
		t.Fatal("expected EventInterrupt")
	}
	if e.ToolName != "get_weather" {
		t.Fatal("tool name mismatch")
	}
	if e.Content != `{"city":"NYC"}` {
		t.Fatal("content mismatch")
	}
	if e.Meta == nil {
		t.Fatal("expected meta")
	}
	if e.Meta["hitl_mode"] != "confirm" {
		t.Fatalf("hitl_mode = %q, want %q", e.Meta["hitl_mode"], "confirm")
	}
	extracted, ok := e.Meta["hitl_info"].(*types.HITLInfo)
	if !ok {
		t.Fatal("expected HITLInfo in meta")
	}
	if extracted.Status != types.HITLStatusPending {
		t.Fatal("status mismatch in meta")
	}
}

func TestImageContent(t *testing.T) {
	ic := &types.ImageContent{Data: "data", MIMEType: "image/png", Detail: "low"}
	if ic.Data != "data" || ic.MIMEType != "image/png" || ic.Detail != "low" {
		t.Fatal("ImageContent fields mismatch")
	}
}

func TestMediaContent(t *testing.T) {
	mc := &types.MediaContent{Data: "data", MIMEType: "audio/mpeg"}
	if mc.Data != "data" || mc.MIMEType != "audio/mpeg" {
		t.Fatal("MediaContent fields mismatch")
	}
}

func TestFileContent(t *testing.T) {
	fc := &types.FileContent{Data: "data", MIMEType: "application/pdf", Name: "doc.pdf"}
	if fc.Data != "data" || fc.MIMEType != "application/pdf" || fc.Name != "doc.pdf" {
		t.Fatal("FileContent fields mismatch")
	}
}

func TestEstimateTokensWithContentParts(t *testing.T) {
	t.Run("image URL part", func(t *testing.T) {
		msgs := []*types.Message{{
			Role: types.RoleUser,
			ContentParts: []types.ContentPart{
				types.NewImageURLPart("https://example.com/long-image-url.jpg"),
			},
		}}
		tokens := types.EstimateTokens(msgs)
		if tokens <= 0 {
			t.Fatal("expected positive token count")
		}
	})

	t.Run("image data part", func(t *testing.T) {
		msgs := []*types.Message{{
			Role: types.RoleUser,
			ContentParts: []types.ContentPart{
				types.NewImageDataPart("base64datahere", "image/png", "auto"),
			},
		}}
		tokens := types.EstimateTokens(msgs)
		if tokens <= 0 {
			t.Fatal("expected positive token count")
		}
	})

	t.Run("unknown content type part", func(t *testing.T) {
		msgs := []*types.Message{{
			Role: types.RoleUser,
			ContentParts: []types.ContentPart{
				{Type: "unknown_type"},
			},
		}}
		tokens := types.EstimateTokens(msgs)
		if tokens <= 0 {
			t.Fatal("expected positive token count for unknown type")
		}
	})
}

func TestEventFields(t *testing.T) {
	e := &types.Event{
		Type:        types.EventToken,
		Content:     "hello",
		ToolName:    "tool",
		ToolArgs:    "{}",
		Usage:       &types.TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		Error:       errors.New("err"),
		Meta:        map[string]any{"k": "v"},
		ParseResult: &types.ParseResult{Raw: "raw"},
		RunPath:     "root/agent",
	}
	if e.Type != types.EventToken || e.Content != "hello" {
		t.Fatal("basic fields mismatch")
	}
	if e.Usage.TotalTokens != 3 {
		t.Fatal("usage mismatch")
	}
	if e.RunPath != "root/agent" {
		t.Fatal("run path mismatch")
	}
}
