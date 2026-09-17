package modelrouter

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// mockModel implements kernel.ChatModel for testing.
type mockModel struct {
	name  string
	err   error
	calls atomic.Int64
}

func (m *mockModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	m.calls.Add(1)
	if m.err != nil {
		return nil, nil, m.err
	}
	return types.NewAssistantMessage("response from " + m.name), &types.TokenUsage{TotalTokens: 10}, nil
}

func (m *mockModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}

func (m *mockModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return len(msgs) * 10, nil
}

// nonChatModel implements only kernel.Model — a misconfigured route.
type nonChatModel struct{ name string }

func (m *nonChatModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return 0, nil
}

// blockingModel blocks until ctx is done — used to verify route timeouts.
type blockingModel struct{ name string }

func (m *blockingModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}
func (m *blockingModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}
func (m *blockingModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return 0, nil
}

// slowModel waits before returning — used to verify race-all picks the fastest.
type slowModel struct {
	name  string
	delay time.Duration
	calls atomic.Int64
}

func (m *slowModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	m.calls.Add(1)
	select {
	case <-time.After(m.delay):
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return types.NewAssistantMessage("slow " + m.name), &types.TokenUsage{TotalTokens: 1}, nil
}
func (m *slowModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}
func (m *slowModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return 0, nil
}

func TestNewModelRouter(t *testing.T) {
	m1 := &mockModel{name: "m1"}
	router := NewModelRouter([]ModelRoute{{Model: m1}})
	if router == nil {
		t.Fatal("router is nil")
	}
	if router.strategy != StrategyPriority {
		t.Errorf("default strategy = %q, want 'priority'", router.strategy)
	}
}

func TestModelRouter_WithStrategy(t *testing.T) {
	m1 := &mockModel{name: "m1"}
	m2 := &mockModel{name: "m2"}
	router := NewModelRouter(
		[]ModelRoute{{Model: m1}, {Model: m2}},
		WithRouterStrategy(StrategyRoundRobin),
	)
	if router.strategy != StrategyRoundRobin {
		t.Errorf("strategy = %q, want 'round_robin'", router.strategy)
	}
}

func TestModelRouterPriority_Success(t *testing.T) {
	m1 := &mockModel{name: "m1"}
	m2 := &mockModel{name: "m2"}
	router := NewModelRouter([]ModelRoute{{Model: m1}, {Model: m2}})

	resp, usage, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if m1.calls.Load() != 1 {
		t.Errorf("m1 called %d times, want 1", m1.calls.Load())
	}
	if m2.calls.Load() != 0 {
		t.Errorf("m2 called %d times, want 0", m2.calls.Load())
	}
	_ = resp
	_ = usage
}

func TestModelRouterPriority_Fallback(t *testing.T) {
	m1 := &mockModel{name: "m1", err: errors.New("m1 fails")}
	m2 := &mockModel{name: "m2"}
	router := NewModelRouter([]ModelRoute{{Model: m1}, {Model: m2}})

	resp, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if m1.calls.Load() != 1 || m2.calls.Load() != 1 {
		t.Errorf("m1=%d m2=%d, want both 1", m1.calls.Load(), m2.calls.Load())
	}
	if resp.Content != "response from m2" {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestModelRouterPriority_AllFail(t *testing.T) {
	m1 := &mockModel{name: "m1", err: errors.New("fail1")}
	m2 := &mockModel{name: "m2", err: errors.New("fail2")}
	router := NewModelRouter([]ModelRoute{{Model: m1}, {Model: m2}})

	_, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err == nil {
		t.Fatal("expected error when all models fail")
	}
}

func TestModelRouterRoundRobin(t *testing.T) {
	m1 := &mockModel{name: "m1"}
	m2 := &mockModel{name: "m2"}
	router := NewModelRouter(
		[]ModelRoute{{Model: m1}, {Model: m2}},
		WithRouterStrategy(StrategyRoundRobin),
	)

	router.Generate(context.Background(), []*types.Message{types.NewUserMessage("q1")})
	router.Generate(context.Background(), []*types.Message{types.NewUserMessage("q2")})

	if m1.calls.Load() != 1 || m2.calls.Load() != 1 {
		t.Errorf("round-robin: m1=%d m2=%d, want both 1", m1.calls.Load(), m2.calls.Load())
	}
}

func TestModelRouter_EmptyRoutes(t *testing.T) {
	router := NewModelRouter(nil)
	_, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err == nil {
		t.Fatal("expected error with empty routes")
	}
}

func TestModelRouter_WithMiddleware(t *testing.T) {
	m1 := &mockModel{name: "m1"}
	called := false

	router := NewModelRouter(
		[]ModelRoute{{Model: m1}},
		WithRouterMiddleware(&testMiddleware{
			wrapFn: func(next kernel.ModelHandler) kernel.ModelHandler {
				return func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
					called = true
					return next(ctx, msgs)
				}
			},
		}),
	)

	_, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !called {
		t.Error("middleware was not called")
	}
}

func TestModelRouter_MiddlewareChain(t *testing.T) {
	m1 := &mockModel{name: "m1"}
	var order []string

	router := NewModelRouter(
		[]ModelRoute{{Model: m1}},
		WithRouterMiddleware(
			&testMiddleware{wrapFn: func(next kernel.ModelHandler) kernel.ModelHandler {
				return func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
					order = append(order, "mw1")
					return next(ctx, msgs)
				}
			}},
			&testMiddleware{wrapFn: func(next kernel.ModelHandler) kernel.ModelHandler {
				return func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
					order = append(order, "mw2")
					return next(ctx, msgs)
				}
			}},
		),
	)

	_, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(order) != 2 || order[0] != "mw1" || order[1] != "mw2" {
		t.Errorf("order = %v, want [mw1 mw2]", order)
	}
}

// ── Robustness ────────────────────────────────────────────────────────────

// TestModelRouter_NonChatModelRoute: a route whose model does not implement
// kernel.ChatModel must surface as an error, never as a panic.
func TestModelRouter_NonChatModelRoute(t *testing.T) {
	router := NewModelRouter([]ModelRoute{{Model: &nonChatModel{name: "bad"}}})

	_, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err == nil {
		t.Fatal("expected error for non-ChatModel route")
	}
	if !strings.Contains(err.Error(), "kernel.ChatModel") {
		t.Fatalf("error = %q, want ChatModel hint", err.Error())
	}
}

// TestModelRouter_EmptyRoutes_ErrNoRoutes: all strategies report ErrNoRoutes.
func TestModelRouter_EmptyRoutes_ErrNoRoutes(t *testing.T) {
	for _, strategy := range []RouterStrategy{StrategyPriority, StrategyRoundRobin, StrategyWeightedRoundRobin, StrategyRaceAll} {
		router := NewModelRouter(nil, WithRouterStrategy(strategy))
		_, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
		if !errors.Is(err, ErrNoRoutes) {
			t.Errorf("%s: err = %v, want ErrNoRoutes", strategy, err)
		}
		if _, err := router.Stream(context.Background(), []*types.Message{types.NewUserMessage("hi")}); !errors.Is(err, ErrNoRoutes) {
			t.Errorf("%s stream: err = %v, want ErrNoRoutes", strategy, err)
		}
	}
}

// TestModelRouter_WeightedRoundRobin: routes are picked proportionally to
// their weights (3:1 over 8 calls => 6:2).
func TestModelRouter_WeightedRoundRobin(t *testing.T) {
	m1 := &mockModel{name: "m1"}
	m2 := &mockModel{name: "m2"}
	router := NewModelRouter(
		[]ModelRoute{{Model: m1, Weight: 3}, {Model: m2, Weight: 1}},
		WithRouterStrategy(StrategyWeightedRoundRobin),
	)

	for i := 0; i < 8; i++ {
		if _, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("q")}); err != nil {
			t.Fatalf("Generate %d: %v", i, err)
		}
	}
	if m1.calls.Load() != 6 || m2.calls.Load() != 2 {
		t.Errorf("calls m1=%d m2=%d, want 6/2 (weight 3:1)", m1.calls.Load(), m2.calls.Load())
	}
}

// TestModelRouter_WeightedZeroWeight: Weight <= 0 falls back to 1.
func TestModelRouter_WeightedZeroWeight(t *testing.T) {
	m1 := &mockModel{name: "m1"}
	m2 := &mockModel{name: "m2"}
	router := NewModelRouter(
		[]ModelRoute{{Model: m1, Weight: 0}, {Model: m2, Weight: 0}},
		WithRouterStrategy(StrategyWeightedRoundRobin),
	)

	for i := 0; i < 4; i++ {
		if _, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("q")}); err != nil {
			t.Fatalf("Generate %d: %v", i, err)
		}
	}
	if m1.calls.Load() != 2 || m2.calls.Load() != 2 {
		t.Errorf("calls m1=%d m2=%d, want 2/2 (equal weights)", m1.calls.Load(), m2.calls.Load())
	}
}

// TestModelRouter_RouteTimeout: a per-route Timeout must bound the call.
func TestModelRouter_RouteTimeout(t *testing.T) {
	router := NewModelRouter([]ModelRoute{{Model: &blockingModel{}, Timeout: 50}})

	start := time.Now()
	_, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected deadline error from blocking model")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout not enforced: elapsed = %v", elapsed)
	}
}

// TestModelRouter_RaceAll_FastestWins: the first successful response wins.
func TestModelRouter_RaceAll_FastestWins(t *testing.T) {
	slow := &slowModel{name: "slow", delay: 300 * time.Millisecond}
	fast := &slowModel{name: "fast", delay: 20 * time.Millisecond}
	router := NewModelRouter(
		[]ModelRoute{{Model: slow}, {Model: fast}},
		WithRouterStrategy(StrategyRaceAll),
	)

	start := time.Now()
	resp, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp == nil || resp.Content != "slow fast" {
		t.Fatalf("content = %v, want the fast route's response", resp)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("race-all should return with the fastest route: %v", elapsed)
	}
}

// TestModelRouter_RaceAll_AllFail: all routes failing aggregates an error.
func TestModelRouter_RaceAll_AllFail(t *testing.T) {
	m1 := &mockModel{name: "m1", err: errors.New("fail1")}
	m2 := &mockModel{name: "m2", err: errors.New("fail2")}
	router := NewModelRouter(
		[]ModelRoute{{Model: m1}, {Model: m2}},
		WithRouterStrategy(StrategyRaceAll),
	)

	_, _, err := router.Generate(context.Background(), []*types.Message{types.NewUserMessage("hi")})
	if err == nil {
		t.Fatal("expected error when all routes fail")
	}
}

// testMiddleware implements the middleware.Middleware interface locally.
type testMiddleware struct {
	wrapFn func(kernel.ModelHandler) kernel.ModelHandler
}

func (m *testMiddleware) Name() string { return "test-mw" }

func (m *testMiddleware) WrapGenerate(next kernel.ModelHandler) kernel.ModelHandler {
	if m.wrapFn != nil {
		return m.wrapFn(next)
	}
	return next
}

func (m *testMiddleware) WrapStream(next kernel.StreamHandler) kernel.StreamHandler {
	return next
}
