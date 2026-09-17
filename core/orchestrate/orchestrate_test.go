package orchestrate

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

// ── mocks ─────────────────────────────────────────────────────────────────

type countingAgent struct {
	name    string
	count   *atomic.Int32
	content string
	err     error
}

func (a *countingAgent) Name() string        { return a.name }
func (a *countingAgent) Description() string { return "counting: " + a.name }
func (a *countingAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	a.count.Add(1)
	select {
	case <-ctx.Done():
		return &kernel.Result{Err: ctx.Err()}
	default:
	}
	return &kernel.Result{
		Content:    a.content,
		Messages:   input.Messages,
		TokenUsage: &types.TokenUsage{TotalTokens: 10},
		Err:        a.err,
	}
}

type echoAgent struct {
	name string
}

func (a *echoAgent) Name() string        { return a.name }
func (a *echoAgent) Description() string { return "echo: " + a.name }
func (a *echoAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	return &kernel.Result{
		Content:  a.name + ": " + lastUserContent(input.Messages),
		Messages: input.Messages,
	}
}

func lastUserContent(msgs []*types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i] != nil && msgs[i].Role == types.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// blockingAgent blocks until released or the context is cancelled, proving
// that batch timeouts propagate cancellation to in-flight agents.
type blockingAgent struct {
	started chan struct{}
	block   chan struct{}
}

func (b *blockingAgent) Name() string        { return "blocker" }
func (b *blockingAgent) Description() string { return "blocking test agent" }
func (b *blockingAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	close(b.started)
	select {
	case <-b.block:
		return &kernel.Result{Content: "done"}
	case <-ctx.Done():
		return &kernel.Result{Err: ctx.Err()}
	}
}

// TestParallel_WithTimeout proves the batch timeout cancels in-flight agents
// instead of leaving the run blocked forever.
func TestParallel_WithTimeout(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	defer close(block)
	a := &blockingAgent{started: started, block: block}
	p := Parallel([]kernel.Agent{a}, WithTimeout(50*time.Millisecond))

	res := p.Run(context.Background(), &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("hi")}}, nil)
	if res.Err == nil {
		t.Fatal("expected timeout error from cancelled agent")
	}
}

type mockTool struct {
	name string
	fn   func(ctx context.Context, args string) (string, error)
}

func (t *mockTool) Name() string                                         { return t.name }
func (t *mockTool) Description() string                                  { return "mock" }
func (t *mockTool) Schema() map[string]any                               { return map[string]any{"type": "object"} }
func (t *mockTool) Run(ctx context.Context, args string) (string, error) { return t.fn(ctx, args) }
func (t *mockTool) ToolMeta() kernel.ToolMeta                            { return kernel.ToolMeta{Kind: kernel.ToolKindFunction} }

type mockRegistry struct {
	tools map[string]kernel.Tool
}

func (r *mockRegistry) List(ctx context.Context) []kernel.Tool {
	out := make([]kernel.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
func (r *mockRegistry) Get(ctx context.Context, name string) kernel.Tool { return r.tools[name] }
func (r *mockRegistry) Add(ctx context.Context, tool kernel.Tool) error {
	r.tools[tool.Name()] = tool
	return nil
}
func (r *mockRegistry) Remove(ctx context.Context, name string) error {
	delete(r.tools, name)
	return nil
}
func (r *mockRegistry) AddSource(ctx context.Context, source kernel.ToolSource) error { return nil }
func (r *mockRegistry) RemoveSource(ctx context.Context, name string) error           { return nil }

// TestExecToolsConcurrentResults proves the error-annotated variant: every
// call yields a message, and failed calls additionally carry the underlying
// execution error so callers can decide whether to retry.
func TestExecToolsConcurrentResults(t *testing.T) {
	registry := &mockRegistry{tools: map[string]kernel.Tool{
		"ok":   &mockTool{name: "ok", fn: func(ctx context.Context, args string) (string, error) { return "done", nil }},
		"fail": &mockTool{name: "fail", fn: func(ctx context.Context, args string) (string, error) { return "", errors.New("boom") }},
	}}

	results := ExecToolsConcurrentResults(context.Background(), []*types.ToolCall{
		{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "ok", Arguments: "{}"}},
		{ID: "c2", Type: "function", Function: types.ToolCallFunction{Name: "fail", Arguments: "{}"}},
	}, registry)

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Err != nil || results[0].Message == nil || results[0].Message.Content != "done" {
		t.Fatalf("ok call: err=%v msg=%v", results[0].Err, results[0].Message)
	}
	if results[1].Err == nil || !strings.Contains(results[1].Message.Content, "boom") {
		t.Fatalf("fail call: err=%v msg=%v", results[1].Err, results[1].Message)
	}

	// The plain variant must stay equivalent (messages only).
	plain := ExecToolsConcurrent(context.Background(), []*types.ToolCall{
		{ID: "c3", Type: "function", Function: types.ToolCallFunction{Name: "fail", Arguments: "{}"}},
	}, registry)
	if len(plain) != 1 || !strings.Contains(plain[0].Content, "boom") {
		t.Fatalf("plain variant: %v", plain)
	}
}

// TestExecToolsConcurrent_CallTimeout: a tool with a declared budget times
// out with a structured TOOL_TIMEOUT error, while tools without a budget
// run under the batch context alone.
func TestExecToolsConcurrent_CallTimeout(t *testing.T) {
	slow := &mockTool{name: "slow", fn: func(ctx context.Context, args string) (string, error) {
		<-ctx.Done() // cooperative: observes the deadline
		return "", ctx.Err()
	}}
	registry := &mockRegistry{tools: map[string]kernel.Tool{
		"slow": slow,
		"fast": &mockTool{name: "fast", fn: func(ctx context.Context, args string) (string, error) { return "fast-done", nil }},
	}}

	results := ExecToolsConcurrentResults(context.Background(), []*types.ToolCall{
		{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "slow", Arguments: "{}"}},
		{ID: "c2", Type: "function", Function: types.ToolCallFunction{Name: "fast", Arguments: "{}"}},
	}, registry, WithCallTimeout(func(name string) time.Duration {
		if name == "slow" {
			return 50 * time.Millisecond
		}
		return 0
	}))

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Err == nil || !errors.Is(results[0].Err, context.DeadlineExceeded) {
		t.Fatalf("slow call err = %v, want deadline exceeded", results[0].Err)
	}
	if !strings.Contains(results[0].Message.Content, "TOOL_TIMEOUT") {
		t.Fatalf("slow call message = %q, want TOOL_TIMEOUT code", results[0].Message.Content)
	}
	if results[1].Err != nil || results[1].Message.Content != "fast-done" {
		t.Fatalf("fast call = %+v, want success", results[1])
	}
}

// ── Parallel ──────────────────────────────────────────────────────────────

func TestParallel_EmptyAgents(t *testing.T) {
	p := Parallel(nil)
	res := p.Run(context.Background(), nil, nil)
	if res.Content != "" || len(res.Messages) > 0 {
		t.Error("empty agents should produce empty result")
	}
}

func TestParallel_SingleAgent(t *testing.T) {
	var c atomic.Int32
	a := &countingAgent{name: "a", content: "done", count: &c}
	p := Parallel([]kernel.Agent{a})

	res := p.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	}, nil)

	if c.Load() != 1 {
		t.Fatalf("agent ran %d times, want 1", c.Load())
	}
	if !strings.Contains(res.Content, "[a] done") {
		t.Fatalf("Content = %q", res.Content)
	}
	if res.TokenUsage == nil || res.TokenUsage.TotalTokens != 10 {
		t.Error("TokenUsage not tracked")
	}
}

func TestParallel_MultiAgent(t *testing.T) {
	var c1, c2 atomic.Int32
	a1 := &countingAgent{name: "a1", content: "one", count: &c1}
	a2 := &countingAgent{name: "a2", content: "two", count: &c2}
	p := Parallel([]kernel.Agent{a1, a2})

	res := p.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	}, nil)

	if c1.Load() != 1 || c2.Load() != 1 {
		t.Fatalf("agents ran %d/%d, want 1/1", c1.Load(), c2.Load())
	}
	if !strings.Contains(res.Content, "[a1] one") {
		t.Error("missing a1 content")
	}
	if !strings.Contains(res.Content, "[a2] two") {
		t.Error("missing a2 content")
	}
	if res.TokenUsage == nil || res.TokenUsage.TotalTokens != 20 {
		t.Fatalf("TokenUsage.TotalTokens = %d, want 20", res.TokenUsage.TotalTokens)
	}
}

func TestParallel_WithMaxConcurrency(t *testing.T) {
	var c1, c2, c3 atomic.Int32
	a1 := &countingAgent{name: "a1", content: "1", count: &c1}
	a2 := &countingAgent{name: "a2", content: "2", count: &c2}
	a3 := &countingAgent{name: "a3", content: "3", count: &c3}
	p := Parallel([]kernel.Agent{a1, a2, a3}, WithMaxConcurrency(1))

	res := p.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	}, nil)

	if c1.Load() != 1 || c2.Load() != 1 || c3.Load() != 1 {
		t.Fatalf("agents ran %d/%d/%d, want 1/1/1", c1.Load(), c2.Load(), c3.Load())
	}
	_ = res
}

func TestParallel_Name(t *testing.T) {
	a1 := &echoAgent{name: "alpha"}
	a2 := &echoAgent{name: "beta"}
	p := Parallel([]kernel.Agent{a1, a2})
	if p.Name() != "parallel:alpha+beta" {
		t.Fatalf("Name = %q", p.Name())
	}
}

func TestParallel_TracksFirstError(t *testing.T) {
	e := errors.New("boom")
	var c atomic.Int32
	a1 := &countingAgent{name: "a1", content: "ok", count: &c}
	a2 := &countingAgent{name: "a2", content: "fail", count: &c, err: e}
	p := Parallel([]kernel.Agent{a1, a2})

	res := p.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	}, nil)
	if res.Err != e {
		t.Fatalf("Err = %v, want %v", res.Err, e)
	}
}

// ── Chain ────────────────────────────────────────────────────────────────

func TestChain_Empty(t *testing.T) {
	c := Chain()
	res := c.Run(context.Background(), nil, nil)
	if res.Content != "" {
		t.Error("empty chain should produce empty result")
	}
}

func TestChain_Single(t *testing.T) {
	e := &echoAgent{name: "e1"}
	c := Chain(e)

	res := c.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hello")},
	}, nil)
	if !strings.Contains(res.Content, "e1: hello") {
		t.Fatalf("Content = %q", res.Content)
	}
}

func TestChain_Multi(t *testing.T) {
	e1 := &echoAgent{name: "e1"}
	e2 := &echoAgent{name: "e2"}
	c := Chain(e1, e2)

	res := c.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hello")},
	}, nil)
	// e2 receives the output of e1 as its input
	if !strings.Contains(res.Content, "e2:") {
		t.Fatalf("Content = %q, expected e2 echo", res.Content)
	}
}

func TestChain_StopsOnError(t *testing.T) {
	e := errors.New("stop")
	a1 := &echoAgent{name: "e1"}
	a2 := &countingAgent{name: "a2", err: e, count: &atomic.Int32{}}
	a3 := &echoAgent{name: "e3"}
	c := Chain(a1, a2, a3)

	res := c.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	}, nil)
	if res.Err != e {
		t.Fatalf("Err = %v, want %v", res.Err, e)
	}
}

func TestChain_Name(t *testing.T) {
	a1 := &echoAgent{name: "alpha"}
	a2 := &echoAgent{name: "beta"}
	c := Chain(a1, a2)
	if c.Name() != "chain:alpha→beta" {
		t.Fatalf("Name = %q", c.Name())
	}
}

// ── Pipe ─────────────────────────────────────────────────────────────────

func TestPipe_WithTransform(t *testing.T) {
	e1 := &echoAgent{name: "e1"}
	e2 := &echoAgent{name: "e2"}
	p := Pipe([]kernel.Agent{e1, e2}, func(res *kernel.Result) *types.AgentInput {
		return &types.AgentInput{
			Messages: []*types.Message{types.NewUserMessage("transformed: " + res.Content)},
		}
	})

	res := p.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("original")},
	}, nil)
	if !strings.Contains(res.Content, "e2: transformed:") {
		t.Fatalf("Content = %q", res.Content)
	}
}

func TestPipe_NilTransform(t *testing.T) {
	e1 := &echoAgent{name: "e1"}
	e2 := &echoAgent{name: "e2"}
	p := Pipe([]kernel.Agent{e1, e2}, nil)

	res := p.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	}, nil)
	if !strings.Contains(res.Content, "e2:") {
		t.Fatalf("Content = %q", res.Content)
	}
}

func TestPipe_Name(t *testing.T) {
	a1 := &echoAgent{name: "alpha"}
	a2 := &echoAgent{name: "beta"}
	p := Pipe([]kernel.Agent{a1, a2}, nil)
	if p.Name() != "pipe:alpha|beta" {
		t.Fatalf("Name = %q", p.Name())
	}
}

// ── Spawn ─────────────────────────────────────────────────────────────────

func TestSpawn_ReturnsResult(t *testing.T) {
	a := &countingAgent{name: "spawned", content: "done", count: &atomic.Int32{}}
	ch := Spawn(context.Background(), a, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("go")},
	}, nil)

	sr := <-ch
	if sr.AgentName != "spawned" {
		t.Fatalf("AgentName = %q", sr.AgentName)
	}
	if sr.Err != nil {
		t.Fatalf("Err = %v", sr.Err)
	}
	if sr.Result.Content != "done" {
		t.Fatalf("Content = %q", sr.Result.Content)
	}
}

func TestSpawn_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &countingAgent{name: "canceled", content: "done", count: &atomic.Int32{}}
	ch := Spawn(ctx, a, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("go")},
	}, nil)

	sr := <-ch
	if sr.Err == nil {
		t.Fatal("expected context canceled error")
	}
}

func TestSpawnAndWait_AllAgentsRun(t *testing.T) {
	var c1, c2 atomic.Int32
	agents := map[string]kernel.Agent{
		"a": &countingAgent{name: "a", count: &c1, content: "A"},
		"b": &countingAgent{name: "b", count: &c2, content: "B"},
	}
	inputs := map[string]*types.AgentInput{
		"a": {Messages: []*types.Message{types.NewUserMessage("xa")}},
		"b": {Messages: []*types.Message{types.NewUserMessage("xb")}},
	}

	results := SpawnAndWait(context.Background(), agents, inputs, nil)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if c1.Load() != 1 || c2.Load() != 1 {
		t.Fatalf("agents ran %d/%d times", c1.Load(), c2.Load())
	}
}

func TestSpawnMap_SharedInput(t *testing.T) {
	var c1, c2 atomic.Int32
	agents := map[string]kernel.Agent{
		"a": &countingAgent{name: "a", count: &c1, content: "A"},
		"b": &countingAgent{name: "b", count: &c2, content: "B"},
	}
	input := &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("shared")},
	}

	results := SpawnMap(context.Background(), agents, input, nil)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if c1.Load() != 1 || c2.Load() != 1 {
		t.Fatalf("agents ran %d/%d times", c1.Load(), c2.Load())
	}
}

// ── ExecToolsConcurrent ───────────────────────────────────────────────────

func TestExecToolsConcurrent_Empty(t *testing.T) {
	reg := &mockRegistry{tools: map[string]kernel.Tool{}}
	msgs := ExecToolsConcurrent(context.Background(), nil, reg)
	if msgs != nil {
		t.Fatal("expected nil for empty tools")
	}
}

func TestExecToolsConcurrent_SingleTool(t *testing.T) {
	reg := &mockRegistry{tools: map[string]kernel.Tool{
		"test_tool": &mockTool{name: "test_tool", fn: func(ctx context.Context, args string) (string, error) {
			return `{"ok":true}`, nil
		}},
	}}

	tools := []*types.ToolCall{
		{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "test_tool", Arguments: `{"x":1}`}},
	}

	msgs := ExecToolsConcurrent(context.Background(), tools, reg)
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs, want 1", len(msgs))
	}
	if msgs[0].Content != `{"ok":true}` {
		t.Fatalf("Content = %q", msgs[0].Content)
	}
	if msgs[0].ToolName != "test_tool" {
		t.Fatalf("ToolName = %q", msgs[0].ToolName)
	}
}

func TestExecToolsConcurrent_ToolNotFound(t *testing.T) {
	reg := &mockRegistry{tools: map[string]kernel.Tool{}}
	tools := []*types.ToolCall{
		{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "missing", Arguments: "{}"}},
	}

	msgs := ExecToolsConcurrent(context.Background(), tools, reg)
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "tool not found") {
		t.Fatalf("Content = %q", msgs[0].Content)
	}
}

func TestExecToolsConcurrent_MultiTool(t *testing.T) {
	reg := &mockRegistry{tools: map[string]kernel.Tool{
		"t1": &mockTool{name: "t1", fn: func(ctx context.Context, args string) (string, error) {
			return "one", nil
		}},
		"t2": &mockTool{name: "t2", fn: func(ctx context.Context, args string) (string, error) {
			return "two", nil
		}},
	}}

	tools := []*types.ToolCall{
		{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "t1", Arguments: "{}"}},
		{ID: "c2", Type: "function", Function: types.ToolCallFunction{Name: "t2", Arguments: "{}"}},
	}

	msgs := ExecToolsConcurrent(context.Background(), tools, reg)
	if len(msgs) != 2 {
		t.Fatalf("got %d msgs, want 2", len(msgs))
	}
}

func TestExecToolsConcurrent_ToolError(t *testing.T) {
	reg := &mockRegistry{tools: map[string]kernel.Tool{
		"err_tool": &mockTool{name: "err_tool", fn: func(ctx context.Context, args string) (string, error) {
			return "", errors.New("simulated error")
		}},
	}}

	tools := []*types.ToolCall{
		{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "err_tool", Arguments: "{}"}},
	}

	msgs := ExecToolsConcurrent(context.Background(), tools, reg)
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "simulated error") {
		t.Fatalf("Content = %q", msgs[0].Content)
	}
}

func TestExecToolsConcurrent_WithConcurrencyLimit(t *testing.T) {
	var concurrent, maxConcurrent atomic.Int32
	reg := &mockRegistry{tools: map[string]kernel.Tool{
		"t1": &mockTool{name: "t1", fn: func(ctx context.Context, args string) (string, error) {
			n := concurrent.Add(1)
			if n > maxConcurrent.Load() {
				maxConcurrent.Store(n)
			}
			concurrent.Add(-1)
			return "ok", nil
		}},
		"t2": &mockTool{name: "t2", fn: func(ctx context.Context, args string) (string, error) {
			n := concurrent.Add(1)
			if n > maxConcurrent.Load() {
				maxConcurrent.Store(n)
			}
			concurrent.Add(-1)
			return "ok", nil
		}},
		"t3": &mockTool{name: "t3", fn: func(ctx context.Context, args string) (string, error) {
			n := concurrent.Add(1)
			if n > maxConcurrent.Load() {
				maxConcurrent.Store(n)
			}
			concurrent.Add(-1)
			return "ok", nil
		}},
	}}

	tools := []*types.ToolCall{
		{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "t1", Arguments: "{}"}},
		{ID: "c2", Type: "function", Function: types.ToolCallFunction{Name: "t2", Arguments: "{}"}},
		{ID: "c3", Type: "function", Function: types.ToolCallFunction{Name: "t3", Arguments: "{}"}},
	}

	msgs := ExecToolsConcurrent(context.Background(), tools, reg, WithToolConcurrency(2))
	if len(msgs) != 3 {
		t.Fatalf("got %d msgs, want 3", len(msgs))
	}
	if maxConcurrent.Load() > 2 {
		t.Fatalf("maxConcurrent = %d, want <= 2", maxConcurrent.Load())
	}
}

// ── mergeResults ──────────────────────────────────────────────────────────

func TestMergeResults_NilHandling(t *testing.T) {
	a := &echoAgent{name: "a"}
	res := mergeResults([]kernel.Agent{a, a}, []*kernel.Result{
		nil,
		{Content: "test", Messages: []*types.Message{types.NewUserMessage("hi")}, TokenUsage: &types.TokenUsage{TotalTokens: 5}},
	})
	if !strings.Contains(res.Content, "[a] test") {
		t.Fatalf("Content = %q", res.Content)
	}
	if res.TokenUsage.TotalTokens != 5 {
		t.Fatalf("TotalTokens = %d", res.TokenUsage.TotalTokens)
	}
}
