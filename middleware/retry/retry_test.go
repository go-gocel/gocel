package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

func TestNew_Defaults(t *testing.T) {
	m := New(0, 0, 0)
	if m.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", m.MaxRetries)
	}
	if m.BaseDelay != time.Second {
		t.Errorf("BaseDelay = %v, want 1s", m.BaseDelay)
	}
	if m.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", m.MaxDelay)
	}
}

// countingHandler wraps a callable model handler and counts invocations.
type countingHandler struct {
	calls atomic.Int32
	fn    func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error)
}

func (h *countingHandler) generate(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
	h.calls.Add(1)
	return h.fn(ctx, msgs)
}

func transientError(attempts int) error {
	if attempts == 0 {
		return errors.New("connection refused")
	}
	return nil
}

func TestWrapGenerate_RetriesTransientThenSucceeds(t *testing.T) {
	m := New(3, time.Millisecond, time.Millisecond)
	var failures int
	h := &countingHandler{fn: func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		if failures < 2 {
			failures++
			return nil, nil, errors.New("upstream 503")
		}
		return types.NewAssistantMessage("ok"), &types.TokenUsage{TotalTokens: 1}, nil
	}}

	resp, usage, err := m.WrapGenerate(h.generate)(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected eventual success: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
	if usage == nil {
		t.Fatal("usage missing")
	}
	if h.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (1 initial + 2 retries)", h.calls.Load())
	}
}

func TestWrapGenerate_NoRetryOnPermanentError(t *testing.T) {
	m := New(3, time.Millisecond, time.Millisecond)
	h := &countingHandler{fn: func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		// A provider-classified permanent failure (C7): auth errors carry
		// Retryable=false and must fail fast.
		return nil, nil, &kernel.ModelError{Code: kernel.CodeModelError, Message: "invalid api key (401)", Retryable: false}
	}}

	_, _, err := m.WrapGenerate(h.generate)(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if h.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (permanent errors must fail fast)", h.calls.Load())
	}
}

func TestWrapGenerate_NoRetryOnCanceled(t *testing.T) {
	m := New(3, time.Millisecond, time.Millisecond)
	h := &countingHandler{fn: func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return nil, nil, context.Canceled
	}}

	_, _, err := m.WrapGenerate(h.generate)(context.Background(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if h.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", h.calls.Load())
	}
}

func TestWrapGenerate_GivesUpAfterMaxRetries(t *testing.T) {
	m := New(2, time.Millisecond, time.Millisecond)
	h := &countingHandler{fn: func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return nil, nil, errors.New("upstream 503")
	}}

	_, _, err := m.WrapGenerate(h.generate)(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if h.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (maxRetries+1)", h.calls.Load())
	}
}

func TestWrapGenerate_HonorsContextCancellation(t *testing.T) {
	m := New(10, time.Hour, time.Hour) // huge delays — must not be hit
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &countingHandler{fn: func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return nil, nil, errors.New("upstream 500")
	}}

	_, _, err := m.WrapGenerate(h.generate)(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestWrapStream_RetriesTransient(t *testing.T) {
	m := New(2, time.Millisecond, time.Millisecond)
	calls := 0
	h := func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("connection refused")
		}
		return &fakeReader{}, nil
	}

	reader, err := m.WrapStream(h)(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected eventual success: %v", err)
	}
	if reader == nil {
		t.Fatal("reader is nil")
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

type fakeReader struct{}

func (r *fakeReader) Recv() (*types.Message, error) { return nil, nil }
func (r *fakeReader) Close() error                  { return nil }
func (r *fakeReader) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// Plain transient errors classify retryable by the kernel default.
		{"timeout", errors.New("request timeout"), true},
		{"refused", errors.New("connection refused"), true},
		// Classified provider errors (C7): status drives retryability.
		{"retryable model error", &kernel.ModelError{Retryable: true}, true},
		{"permanent model error", &kernel.ModelError{Retryable: false}, false},
		{"framework rate limit", kernel.ErrRateLimited, true},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"input error", kernel.NewInputError("args", "bad", nil, "bad arguments"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryable(c.err); got != c.want {
				t.Errorf("isRetryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	m := New(3, time.Second, 10*time.Second)
	b1 := m.backoff(1)
	b2 := m.backoff(2)
	b3 := m.backoff(3)
	if b1 != time.Second || b2 != 2*time.Second || b3 != 4*time.Second {
		t.Errorf("backoff = %v/%v/%v, want 1s/2s/4s", b1, b2, b3)
	}

	// Cap at MaxDelay.
	if got := m.backoff(10); got != 10*time.Second {
		t.Errorf("backoff(10) = %v, want MaxDelay", got)
	}

	// Huge attempt must not overflow into a negative delay.
	huge := New(100, time.Second, time.Minute)
	if got := huge.backoff(100); got <= 0 || got > time.Minute {
		t.Errorf("backoff(100) = %v, want positive and capped", got)
	}
}

// TestJitter_StaysWithinBounds: symmetric jitter keeps every delay inside
// [d*(1-r), d*(1+r)] — the anti-thundering-herd property DSH validated.
func TestJitter_StaysWithinBounds(t *testing.T) {
	m := NewWithOptions(
		WithJitterRatio(0.1),
		func(mw *Middleware) { mw.BaseDelay = time.Second; mw.MaxDelay = time.Hour },
	)
	for i := 0; i < 1000; i++ {
		d := m.delayFor(3, nil) // backoff(3) = 4s
		if d < 3600*time.Millisecond || d > 4400*time.Millisecond {
			t.Fatalf("jittered delay = %v, outside [3.6s, 4.4s]", d)
		}
	}
	// Zero jitter means exact backoff.
	m2 := NewWithOptions(WithJitterRatio(0))
	m2.BaseDelay = time.Second
	m2.MaxDelay = time.Hour
	if d := m2.delayFor(3, nil); d != 4*time.Second {
		t.Fatalf("zero-jitter delay = %v, want exactly 4s", d)
	}
}

// TestProviderRetryAfter_Wins: a provider-suggested wait (RateLimitError.
// RetryAfter) replaces the local backoff for that attempt, capped at
// MaxDelay (DSH providerRetryAfterMs semantics).
func TestProviderRetryAfter_Wins(t *testing.T) {
	m := NewWithOptions(
		func(mw *Middleware) { mw.BaseDelay = time.Millisecond; mw.MaxDelay = 30 * time.Second },
	)
	// Within cap: the provider's wait is used verbatim (no jitter).
	rl := kernel.NewRateLimitError("deepseek", 5)
	if d := m.delayFor(1, rl); d != 5*time.Second {
		t.Fatalf("provider wait = %v, want exactly 5s", d)
	}
	// Beyond cap: clamped to MaxDelay.
	huge := kernel.NewRateLimitError("deepseek", 999)
	if d := m.delayFor(1, huge); d != 30*time.Second {
		t.Fatalf("provider wait beyond cap = %v, want MaxDelay", d)
	}
	// No rate-limit error: local backoff applies (with default ±10% jitter,
	// so assert the bound rather than an exact value).
	if d := m.delayFor(1, errors.New("boom")); d < 900*time.Microsecond || d > 1100*time.Microsecond {
		t.Fatalf("no provider wait = %v, want ~1ms local backoff", d)
	}
}

// TestAlwaysMode_RetriesWithoutLimit: ModeAlways retries every retryable
// failure until success or cancellation.
func TestAlwaysMode_RetriesWithoutLimit(t *testing.T) {
	m := NewWithOptions(WithMode(ModeAlways))
	m.BaseDelay = time.Millisecond
	m.MaxDelay = time.Millisecond

	var calls atomic.Int32
	h := &countingHandler{fn: func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		if calls.Add(1) < 20 {
			return nil, nil, kernel.NewModelError("m", errors.New("transient"), true, "flaky")
		}
		return types.NewAssistantMessage("done"), &types.TokenUsage{}, nil
	}}
	resp, _, err := m.WrapGenerate(h.generate)(context.Background(), nil)
	if err != nil {
		t.Fatalf("always mode = %v, want eventual success", err)
	}
	if resp == nil || resp.Content != "done" {
		t.Fatalf("resp = %+v, want done", resp)
	}
	if calls.Load() != 20 {
		t.Fatalf("calls = %d, want 20 (beyond any finite budget)", calls.Load())
	}
}

// TestAlwaysMode_StopsOnCancellation: cancellation is never retried even in
// always mode.
func TestAlwaysMode_StopsOnCancellation(t *testing.T) {
	m := NewWithOptions(WithMode(ModeAlways))
	m.BaseDelay = time.Millisecond
	m.MaxDelay = time.Millisecond
	h := &countingHandler{fn: func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return nil, nil, context.Canceled
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.WrapGenerate(h.generate)(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v, want context.Canceled", err)
	}
}
