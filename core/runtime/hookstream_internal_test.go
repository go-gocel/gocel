package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ── fake stream / model / registry ──────────────────────────────────────

type fakeRawStream struct {
	closed bool
	msgs   []*types.Message
}

func (f *fakeRawStream) Recv() (*types.Message, error) {
	if len(f.msgs) > 0 {
		m := f.msgs[0]
		f.msgs = f.msgs[1:]
		return m, nil
	}
	return nil, io.EOF
}
func (f *fakeRawStream) Close() error { f.closed = true; return nil }
func (f *fakeRawStream) Done() <-chan struct{} { return nil }

type countModel struct {
	calls int
	err   error
}

func (m *countModel) CountTokens(context.Context, []*types.Message, ...kernel.GenOption) (int, error) {
	return 0, nil
}
func (m *countModel) Generate(context.Context, []*types.Message, ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	m.calls++
	return nil, nil, m.err
}
func (m *countModel) Stream(context.Context, []*types.Message, ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, nil
}

type fakeToolReg struct{ t kernel.Tool }

func (f *fakeToolReg) List(context.Context) []kernel.Tool {
	if f.t != nil {
		return []kernel.Tool{f.t}
	}
	return nil
}
func (f *fakeToolReg) Get(context.Context, string) kernel.Tool { return f.t }
func (f *fakeToolReg) Add(context.Context, kernel.Tool) error  { return nil }
func (f *fakeToolReg) Remove(context.Context, string) error    { return nil }

// ── regression tests ────────────────────────────────────────────────────

// TestHookStreamReader_EOFClosesUnderlying: at EOF the wrapper must release
// the underlying stream — a later Close() is a no-op, so without this the
// HTTP body never closes (C5).
func TestHookStreamReader_EOFClosesUnderlying(t *testing.T) {
	raw := &fakeRawStream{}
	r := &hookStreamReader{ctx: context.Background(), raw: raw}
	if _, err := r.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("first Recv: %v", err)
	}
	if !raw.closed {
		t.Fatalf("underlying stream not closed after EOF")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close after EOF: %v", err)
	}
}

// TestHookStreamReader_EarlyCloseClosesUnderlying: Close before EOF must
// still release the underlying stream exactly once.
func TestHookStreamReader_EarlyCloseClosesUnderlying(t *testing.T) {
	raw := &fakeRawStream{}
	r := &hookStreamReader{ctx: context.Background(), raw: raw}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !raw.closed {
		t.Fatalf("underlying stream not closed on early Close")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestExecTools_BlockedMessageValidJSON: guard-blocked tool messages must
// carry a JSON-parseable payload — raw error text breaks the message JSON
// fed back to the provider (C6).
func TestExecTools_BlockedMessageValidJSON(t *testing.T) {
	rt := NewRuntime(nil, &fakeToolReg{})
	rt.OnToolCall(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		return ctx, info, errors.New(`blocked "quoted" and 
newline`)
	})
	msgs := rt.ExecTools(context.Background(), []*types.ToolCall{
		{ID: "1", Function: types.ToolCallFunction{Name: "t", Arguments: "{}"}},
	})
	if len(msgs) != 1 || msgs[0] == nil {
		t.Fatalf("expected 1 blocked tool message, got %d", len(msgs))
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(msgs[0].Content), &v); err != nil {
		t.Fatalf("blocked message content is not valid JSON: %v\ncontent: %s", err, msgs[0].Content)
	}
	if !strings.Contains(v["error"], "blocked") {
		t.Fatalf("error payload missing: %v", v)
	}
}

// TestCallModel_NegativeMaxRetries: a negative retry budget must not skip
// the model call entirely (loop `attempt <= MaxRetries` with -1 never
// executes) or report a bogus "%!w(<nil>)" error (C21-adjacent).
func TestCallModel_NegativeMaxRetries(t *testing.T) {
	m := &countModel{err: errors.New("boom")}
	rt := NewRuntime(m, nil)
	rt.Config.RetryPolicy = RetryPolicy{MaxRetries: -1, RetryOnModel: true}
	_, _, err := rt.CallModel(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if m.calls != 1 {
		t.Fatalf("model called %d times, want 1", m.calls)
	}
	if err == nil || !strings.Contains(err.Error(), "after 0 retries") {
		t.Fatalf("unexpected error: %v", err)
	}
}
