package runner

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/types"
)

// ── Stream ───────────────────────────────────────────────────────────────
// Stream 是 Runner 的独立执行入口（与 Run 共享终态事件契约），其用例
// 统一集中于本文件。
// Stream is Runner's separate execution entry point; it shares the terminal
// event contract with Run, and all its cases live here.

// TestStream_DoesNotMutateCallerInput: Stream wires the sender on a copy —
// the caller-owned input must stay untouched (C2), so reuse and concurrent
// use of the original stay race-free.
func TestStream_DoesNotMutateCallerInput(t *testing.T) {
	fa := &fakeAgent{name: "a"}
	r := NewRunner(fa, nil)
	input := &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}}

	h, err := r.Stream(context.Background(), input)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if input.EnableStreaming {
		t.Fatalf("Stream mutated caller input: EnableStreaming = true")
	}
	if input.StreamSender != nil {
		t.Fatalf("Stream mutated caller input: StreamSender set")
	}
	if got := h.Result(); got == nil || got.Err != nil {
		t.Fatalf("stream result: %+v", got)
	}
	// The agent must have received the wired copy.
	if fa.lastInput == input {
		t.Fatalf("agent received the caller's input pointer, want a copy")
	}
	if fa.lastInput == nil || !fa.lastInput.EnableStreaming || fa.lastInput.StreamSender == nil {
		t.Fatalf("agent input not wired: %+v", fa.lastInput)
	}
}

// TestStream_ContextCancelEndsWithErrorEvent (D3): canceling the stream's
// context interrupts the run and the handle drains with an error event.
func TestStream_ContextCancelEndsWithErrorEvent(t *testing.T) {
	r := NewRunner(&blockingAgent{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	handle, err := r.Stream(ctx, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	cancel()
	var sawError bool
	for {
		ev, ok := handle.Next()
		if !ok {
			break
		}
		if ev.Type == types.EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("an interrupted stream must deliver an error event")
	}
}
