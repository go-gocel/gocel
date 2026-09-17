package logging

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

func TestNew_DefaultLogger(t *testing.T) {
	m := New(nil)
	if m == nil || m.Logger == nil {
		t.Fatal("New(nil) should install the default logger")
	}
	if m.Name() != "logging" {
		t.Errorf("Name = %q, want 'logging'", m.Name())
	}
}

func TestWrapGenerate_LogsSuccess(t *testing.T) {
	var buf bytes.Buffer
	m := New(log.New(&buf, "", 0))

	resp := types.NewAssistantMessage("hello")
	handler := m.WrapGenerate(func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return resp, &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, nil
	})

	out, usage, err := handler(context.Background(), nil)
	if err != nil || out != resp {
		t.Fatalf("handler: out=%v err=%v", out, err)
	}
	if usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", usage)
	}
	logged := buf.String()
	if !strings.Contains(logged, "generate OK") ||
		!strings.Contains(logged, "in=10 out=5 total=15") {
		t.Fatalf("missing success log, got: %s", logged)
	}
}

func TestWrapGenerate_LogsError(t *testing.T) {
	var buf bytes.Buffer
	m := New(log.New(&buf, "", 0))

	handler := m.WrapGenerate(func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return nil, nil, errors.New("boom")
	})

	_, _, err := handler(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(buf.String(), "generate ERROR") {
		t.Fatalf("missing error log, got: %s", buf.String())
	}
}

func TestWrapGenerate_PassesThroughMessages(t *testing.T) {
	var buf bytes.Buffer
	m := New(log.New(&buf, "", 0))

	msgs := []*types.Message{types.NewUserMessage("hi")}
	handler := m.WrapGenerate(func(ctx context.Context, in []*types.Message) (*types.Message, *types.TokenUsage, error) {
		if len(in) != 1 {
			t.Fatalf("messages not passed through: %d", len(in))
		}
		return types.NewAssistantMessage("ok"), nil, nil
	})

	if _, _, err := handler(context.Background(), msgs); err != nil {
		t.Fatalf("handler: %v", err)
	}
}

func TestWrapStream_LogsStart(t *testing.T) {
	var buf bytes.Buffer
	m := New(log.New(&buf, "", 0))

	handler := m.WrapStream(func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		return &fakeStream{}, nil
	})

	reader, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if reader == nil {
		t.Fatal("reader is nil")
	}
	if !strings.Contains(buf.String(), "stream  START") {
		t.Fatalf("missing stream start log, got: %s", buf.String())
	}
}

type fakeStream struct{}

func (s *fakeStream) Recv() (*types.Message, error) { return nil, nil }
func (s *fakeStream) Close() error                  { return nil }
func (s *fakeStream) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// ── Stream completion logging ───────────────────────────────────────────

// recvStep 是 fakeSeqStream 的脚本化一步。
type recvStep struct {
	chunk *types.Message
	err   error
}

// fakeSeqStream 按脚本顺序返回 chunk/错误，脚本耗尽后返回 io.EOF。
type fakeSeqStream struct {
	steps []recvStep
	i     int
}

func (s *fakeSeqStream) Recv() (*types.Message, error) {
	if s.i >= len(s.steps) {
		return nil, io.EOF
	}
	st := s.steps[s.i]
	s.i++
	return st.chunk, st.err
}
func (s *fakeSeqStream) Close() error { return nil }
func (s *fakeSeqStream) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// TestWrapStream_LogsCompletionOnEOF 验证流完整读完（EOF）后补记完成事件：
// 耗时、字符数与 token 用量。
func TestWrapStream_LogsCompletionOnEOF(t *testing.T) {
	var buf bytes.Buffer
	m := New(log.New(&buf, "", 0))

	handler := m.WrapStream(func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		return &fakeSeqStream{steps: []recvStep{
			{chunk: &types.Message{Content: "hello", Meta: map[string]any{
				"usage": &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			}}},
			{chunk: &types.Message{Content: " world"}},
		}}, nil
	})

	reader, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if _, err := reader.Recv(); err != nil {
		t.Fatalf("recv 1: %v", err)
	}
	if _, err := reader.Recv(); err != nil {
		t.Fatalf("recv 2: %v", err)
	}
	if _, err := reader.Recv(); err != io.EOF {
		t.Fatalf("recv 3 err = %v, want io.EOF", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "stream  OK") {
		t.Fatalf("missing stream OK log, got: %s", logged)
	}
	if !strings.Contains(logged, "chars=11") {
		t.Fatalf("missing char count, got: %s", logged)
	}
	if !strings.Contains(logged, "in=10 out=5 total=15") {
		t.Fatalf("missing usage, got: %s", logged)
	}
}

// TestWrapStream_LogsErrorMidStream 验证流中途出错时补记错误事件。
func TestWrapStream_LogsErrorMidStream(t *testing.T) {
	var buf bytes.Buffer
	m := New(log.New(&buf, "", 0))

	handler := m.WrapStream(func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		return &fakeSeqStream{steps: []recvStep{
			{chunk: &types.Message{Content: "partial"}},
			{err: errors.New("boom")},
		}}, nil
	})

	reader, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if _, err := reader.Recv(); err != nil {
		t.Fatalf("recv 1: %v", err)
	}
	if _, err := reader.Recv(); err == nil {
		t.Fatal("recv 2 should return the scripted error")
	}

	logged := buf.String()
	if !strings.Contains(logged, "stream  ERROR") || !strings.Contains(logged, "boom") {
		t.Fatalf("missing stream error log, got: %s", logged)
	}
}

// TestWrapStream_LogsAbortOnClose 验证未读满即关闭时补记中止事件。
func TestWrapStream_LogsAbortOnClose(t *testing.T) {
	var buf bytes.Buffer
	m := New(log.New(&buf, "", 0))

	handler := m.WrapStream(func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		return &fakeSeqStream{steps: []recvStep{{chunk: &types.Message{Content: "x"}}}}, nil
	})

	reader, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if _, err := reader.Recv(); err != nil {
		t.Fatalf("recv: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if !strings.Contains(buf.String(), "stream  ABORT") {
		t.Fatalf("missing stream abort log, got: %s", buf.String())
	}
}

// TestWrapStream_NoDoubleCompletion 验证 EOF 后再 Close 只记一次完成事件。
func TestWrapStream_NoDoubleCompletion(t *testing.T) {
	var buf bytes.Buffer
	m := New(log.New(&buf, "", 0))

	handler := m.WrapStream(func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		return &fakeSeqStream{steps: []recvStep{{chunk: &types.Message{Content: "x"}}}}, nil
	})

	reader, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if _, err := reader.Recv(); err != nil {
		t.Fatalf("recv 1: %v", err)
	}
	if _, err := reader.Recv(); err != io.EOF {
		t.Fatalf("recv 2 err = %v, want io.EOF", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if n := strings.Count(buf.String(), "stream  OK"); n != 1 {
		t.Fatalf("OK logged %d times, want 1; log: %s", n, buf.String())
	}
	if strings.Contains(buf.String(), "stream  ABORT") {
		t.Fatalf("unexpected ABORT after EOF; log: %s", buf.String())
	}
}
