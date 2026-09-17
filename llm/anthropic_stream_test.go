package llm

import (
	"io"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Regression (empirically verified defect): Anthropic's streamed tool-call
// arguments arrive as input_json_delta.partial_json — the old code only
// read Delta.Text, so every streamed tool call got "{}".
func TestAnthropicStream_AssemblesToolCallArgs(t *testing.T) {
	body := strings.NewReader("" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"get_weather\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"San\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\" Francisco\\\"}\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	r := newAnthropicStreamReader(io.NopCloser(body))
	var last *types.Message
	for {
		msg, err := r.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		last = msg
	}
	if last == nil || len(last.ToolCalls) != 1 {
		t.Fatalf("expected one assembled tool call, got %+v", last)
	}
	tc := last.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Function.Name != "get_weather" {
		t.Fatalf("tool call identity wrong: %+v", tc)
	}
	if tc.Function.Arguments != `{"city":"San Francisco"}` {
		t.Fatalf("arguments = %q, want the accumulated partial_json (empty {} seed must not leak)", tc.Function.Arguments)
	}
}

// Regression: a stream that ends before message_stop is a truncated stream,
// not a clean end — pending tool calls must not silently vanish.
func TestAnthropicStream_PrematureEOFIsError(t *testing.T) {
	body := strings.NewReader("" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"f\",\"input\":{}}}\n\n")
	r := newAnthropicStreamReader(io.NopCloser(body))
	if _, err := r.Recv(); err == nil || err == io.EOF {
		t.Fatalf("premature EOF = %v, want a truncation error", err)
	}
}

// Regression: "data:" without a space was silently skipped, yielding an
// empty stream; and EOF before [DONE] must not masquerade as clean end.
func TestOpenAIStream_SpaceLessDataAndPrematureEOF(t *testing.T) {
	body := strings.NewReader("data:{\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata:[DONE]\n\n")
	r := newOpenAIStreamReader(io.NopCloser(body))
	msg, err := r.Recv()
	if err != nil {
		t.Fatalf("space-less data: %v", err)
	}
	if msg == nil || msg.Content != "hi" {
		t.Fatalf("content = %+v, want \"hi\"", msg)
	}
	if _, err := r.Recv(); err != io.EOF {
		t.Fatalf("after [DONE] = %v, want EOF", err)
	}

	// Premature EOF (no [DONE], no finish_reason) must error.
	body2 := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	r2 := newOpenAIStreamReader(io.NopCloser(body2))
	if _, err := r2.Recv(); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if _, err := r2.Recv(); err == nil || err == io.EOF {
		t.Fatalf("premature EOF = %v, want a truncation error", err)
	}
}

// Regression: text carried alongside a tool-call delta was dropped.
func TestOpenAIStream_TextWithToolDeltaNotDropped(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Let me check\",\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
	r := newOpenAIStreamReader(io.NopCloser(body))
	msgs := drainAll(r)
	var textSeen, toolSeen bool
	for _, m := range msgs {
		if m.Content != "" {
			textSeen = true
		}
		if len(m.ToolCalls) > 0 {
			toolSeen = true
		}
	}
	if !textSeen || !toolSeen {
		t.Fatalf("text=%v tool=%v — text alongside a tool delta was dropped", textSeen, toolSeen)
	}
}

func drainAll(r kernel.StreamReader) []*types.Message {
	var out []*types.Message
	for {
		m, err := r.Recv()
		if err == io.EOF {
			return out
		}
		if err != nil {
			return out
		}
		out = append(out, m)
	}
}
