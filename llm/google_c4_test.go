package llm

import (
	"io"
	"strings"
	"testing"
)

// TestGeminiStreamReader_StripsSSEDataPrefix is the C4 regression: with
// alt=sse Gemini frames chunks as "data: {...}" SSE lines; the reader used
// to parse lines as plain JSON, skip every frame, and deliver a silent
// empty stream.
func TestGeminiStreamReader_StripsSSEDataPrefix(t *testing.T) {
	body := strings.NewReader(
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}],\"role\":\"model\"}}]}\n\n" +
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" world\"}],\"role\":\"model\"}}]}\n\n",
	)
	r := newGeminiStreamReader(io.NopCloser(body))

	msg, err := r.Recv()
	if err != nil || msg == nil || msg.Content != "hello" {
		t.Fatalf("first Recv = %v, %v; want hello (SSE frames silently dropped — C4)", msg, err)
	}
	msg, err = r.Recv()
	if err != nil || msg == nil || msg.Content != " world" {
		t.Fatalf("second Recv = %v, %v; want ' world'", msg, err)
	}
	if _, err := r.Recv(); err != io.EOF {
		t.Fatalf("third Recv = %v, want io.EOF", err)
	}
}

// TestGeminiStreamReader_ToleratesPlainJSON pins the non-regression side:
// deployments serving plain JSON lines (no data: prefix) keep working.
func TestGeminiStreamReader_ToleratesPlainJSON(t *testing.T) {
	body := strings.NewReader(
		"{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"plain\"}],\"role\":\"model\"}}]}\n",
	)
	r := newGeminiStreamReader(io.NopCloser(body))

	msg, err := r.Recv()
	if err != nil || msg == nil || msg.Content != "plain" {
		t.Fatalf("Recv = %v, %v; want plain", msg, err)
	}
}
