package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
	"github.com/go-gocel/gocel/core/types"
)

// ── mocks ───────────────────────────────────────────────────────────────

type stubRuntime struct{}

func (s *stubRuntime) CallModel(context.Context, []*types.Message, ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return nil, nil, errors.New("unused")
}
func (s *stubRuntime) CallModelStream(context.Context, []*types.Message, ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("unused")
}
func (s *stubRuntime) ExecTools(context.Context, []*types.ToolCall) []*types.Message { return nil }
func (s *stubRuntime) CountTokens(context.Context, []*types.Message, ...kernel.GenOption) (int, error) {
	return 0, nil
}
func (s *stubRuntime) ListTools(context.Context) []kernel.Tool { return nil }
func (s *stubRuntime) Register(context.Context, kernel.ToolProvider) error {
	return nil
}
func (s *stubRuntime) Unregister(context.Context, string) error { return nil }
func (s *stubRuntime) State() kernel.StateManager               { return nil }
func (s *stubRuntime) FireAgentStart(ctx context.Context, info *kernel.AgentRunInfo) (context.Context, *kernel.AgentRunInfo, error) {
	return ctx, info, nil
}
func (s *stubRuntime) FireAgentEnd(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
	return ctx, info, nil
}
func (s *stubRuntime) FireMessagesBuilt(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
	return ctx, msgs, nil
}
func (s *stubRuntime) FireStepStart(ctx context.Context, info *kernel.StepInfo) (context.Context, bool, *kernel.StepInfo, error) {
	return ctx, true, info, nil
}
func (s *stubRuntime) FireStepEnd(ctx context.Context, info *kernel.StepInfo) (context.Context, bool, *kernel.StepInfo, error) {
	return ctx, true, info, nil
}
func (s *stubRuntime) FireDecision(ctx context.Context, info *kernel.DecisionInfo) error {
	return nil
}

// scriptChild returns a fixed JSON (or failure) per call; it records the
// prompt it received and the number of concurrently running children.
type scriptChild struct {
	content string
	fail    bool
	block   chan struct{}

	prompts []string
	mu      sync.Mutex

	active atomic.Int64
	peak   atomic.Int64
}

func (a *scriptChild) Name() string        { return "child" }
func (a *scriptChild) Description() string { return "scripted" }
func (a *scriptChild) Run(ctx context.Context, input *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	now := a.active.Add(1)
	for {
		peak := a.peak.Load()
		if now <= peak || a.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	defer a.active.Add(-1)

	content := ""
	if len(input.Messages) > 0 {
		content = input.Messages[0].Content
	}
	a.mu.Lock()
	a.prompts = append(a.prompts, content)
	a.mu.Unlock()
	if a.block != nil {
		select {
		case <-a.block:
		case <-ctx.Done():
			return &kernel.Result{Err: ctx.Err()}
		}
	}
	if a.fail {
		return &kernel.Result{Err: errors.New("child failed the task")}
	}
	return &kernel.Result{Content: a.content}
}

func (a *scriptChild) promptList() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

func newEngine(t *testing.T, factory func(ctx context.Context) kernel.Agent, cfg Config) (*Engine, *orchestrate.Registry, context.Context) {
	t.Helper()
	if cfg.Registry == nil {
		cfg.Registry = orchestrate.NewRegistry()
	}
	if cfg.Factory == nil {
		cfg.Factory = factory
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := kernel.WithRuntime(context.Background(), &stubRuntime{})
	return e, cfg.Registry, ctx
}

func parse(t *testing.T, e *Engine, raw string) *Spec {
	t.Helper()
	spec, err := e.ParseSpec([]byte(raw))
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	return spec
}

// ── spec validation ─────────────────────────────────────────────────────

const validSpec = `{
  "meta": {"name": "audit", "description": "audit files", "phases": [{"title": "scan"}]},
  "items": ["a.go", "b.go"],
  "stages": [{"title": "analyze", "prompt": "Analyze {{item}} (index {{index}}).",
              "schema": {"type": "object", "properties": {"ok": {"type": "boolean"}}, "required": ["ok"]}}]
}`

func TestParseSpec_Valid(t *testing.T) {
	e, _, _ := newEngine(t, func(context.Context) kernel.Agent { return nil }, Config{})
	spec := parse(t, e, validSpec)
	if spec.Meta.Name != "audit" || len(spec.Items) != 2 || len(spec.Stages) != 1 {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestParseSpec_MetaRequired(t *testing.T) {
	e, _, _ := newEngine(t, func(context.Context) kernel.Agent { return nil }, Config{})
	raw := `{"meta": {"name": ""}, "items": [], "stages": [{"title": "s", "prompt": "p"}]}`
	if _, err := e.ParseSpec([]byte(raw)); !IsErrorCode(err, CodeInvalidSpec) {
		t.Fatalf("missing description must be CodeInvalidSpec, got %v", err)
	}
	raw = `{"meta": {"description": "d"}, "items": [], "stages": [{"title": "s", "prompt": "p"}]}`
	if _, err := e.ParseSpec([]byte(raw)); !IsErrorCode(err, CodeInvalidSpec) {
		t.Fatalf("missing name must be CodeInvalidSpec, got %v", err)
	}
}

func TestParseSpec_UnsupportedSchemaKeyword(t *testing.T) {
	e, _, _ := newEngine(t, func(context.Context) kernel.Agent { return nil }, Config{})
	raw := `{"meta": {"name": "n", "description": "d"}, "items": ["x"],
	         "stages": [{"title": "s", "prompt": "p", "schema": {"type": "string", "pattern": ".*"}}]}`
	if _, err := e.ParseSpec([]byte(raw)); !IsErrorCode(err, CodeUnsupportedSchema) {
		t.Fatalf("pattern keyword must be CodeUnsupportedSchema, got %v", err)
	}
}

func TestParseSpec_Caps(t *testing.T) {
	e, _, _ := newEngine(t, func(context.Context) kernel.Agent { return nil }, Config{MaxItemsPerCall: 2, MaxTotalAgents: 4})
	raw := `{"meta": {"name": "n", "description": "d"}, "items": [1,2,3],
	         "stages": [{"title": "s", "prompt": "p"}]}`
	if _, err := e.ParseSpec([]byte(raw)); !IsErrorCode(err, CodeItemCap) {
		t.Fatalf("3 items over cap 2 must be CodeItemCap, got %v", err)
	}
	raw = `{"meta": {"name": "n", "description": "d"}, "items": [1,2],
	       "stages": [{"title": "s", "prompt": "p"}, {"title": "s2", "prompt": "p2"}, {"title": "s3", "prompt": "p3"}]}`
	if _, err := e.ParseSpec([]byte(raw)); !IsErrorCode(err, CodeAgentCap) {
		t.Fatalf("6 stage-items over cap 4 must be CodeAgentCap, got %v", err)
	}
}

// ── schema validation ───────────────────────────────────────────────────

func TestSchemaValidate_ObjectRules(t *testing.T) {
	s := &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"ok": {Type: "boolean"},
		},
		Required:             []string{"ok"},
		AdditionalProperties: boolPtr(false),
	}
	if err := s.validate(map[string]any{"ok": true}); err != nil {
		t.Fatalf("valid object rejected: %v", err)
	}
	if err := s.validate(map[string]any{"ok": true, "extra": 1}); err == nil {
		t.Fatal("additionalProperties=false must reject extras")
	}
	if err := s.validate(map[string]any{"extra": 1}); err == nil {
		t.Fatal("missing required property must be rejected")
	}
	if err := s.validate(map[string]any{"ok": "yes"}); err == nil {
		t.Fatal("wrong property type must be rejected")
	}
}

func TestSchemaValidate_EnumConstOneOfItems(t *testing.T) {
	if err := (&Schema{Enum: []any{"a", "b"}}).validate("a"); err != nil {
		t.Fatalf("enum member rejected: %v", err)
	}
	if err := (&Schema{Enum: []any{"a", "b"}}).validate("c"); err == nil {
		t.Fatal("non-member must be rejected")
	}
	if err := (&Schema{Const: 42.0}).validate(42.0); err != nil {
		t.Fatalf("const match rejected: %v", err)
	}
	if err := (&Schema{Const: 42.0}).validate(43.0); err == nil {
		t.Fatal("const mismatch must be rejected")
	}
	oneOf := &Schema{OneOf: []*Schema{{Type: "string"}, {Type: "integer"}}}
	if err := oneOf.validate("x"); err != nil {
		t.Fatalf("oneOf string rejected: %v", err)
	}
	if err := oneOf.validate(true); err == nil {
		t.Fatal("oneOf boolean must be rejected")
	}
	if err := (&Schema{Type: "array", Items: &Schema{Type: "integer"}}).validate([]any{1.0, 2.0}); err != nil {
		t.Fatalf("array of integers rejected: %v", err)
	}
	if err := (&Schema{Type: "array", Items: &Schema{Type: "integer"}}).validate([]any{1.0, "x"}); err == nil {
		t.Fatal("array item type mismatch must be rejected")
	}
}

// ── engine run ──────────────────────────────────────────────────────────

func TestRun_HappyPath(t *testing.T) {
	child := &scriptChild{content: `{"ok":true}`}
	e, _, ctx := newEngine(t, func(context.Context) kernel.Agent { return child }, Config{})
	spec := parse(t, e, validSpec)
	res, err := e.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.AgentsStarted != 2 || len(res.Items) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Items[0].Values[0].(map[string]any)["ok"] != true {
		t.Fatalf("validated value = %v", res.Items[0].Values[0])
	}
	prompts := child.promptList()
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(prompts))
	}
	// Items run concurrently: order is not guaranteed, contents are.
	seen := map[string]bool{}
	for _, p := range prompts {
		seen[p] = true
	}
	if !seen["Analyze \"a.go\" (index 0)."] || !seen["Analyze \"b.go\" (index 1)."] {
		t.Fatalf("prompt rendering wrong: %q", prompts)
	}
}

func TestRun_SchemaMismatchNullsItemOnly(t *testing.T) {
	n := 0
	mu := sync.Mutex{}
	e, _, ctx := newEngine(t, func(context.Context) kernel.Agent {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 2 {
			return &scriptChild{content: `not json`}
		}
		return &scriptChild{content: `{"ok":true}`}
	}, Config{})
	spec := parse(t, e, validSpec)
	res, err := e.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Which item gets the invalid child depends on goroutine scheduling —
	// assert exactly one null item and one valid item, not their order.
	var nulls, valids int
	for _, item := range res.Items {
		if item.Values[0] == nil {
			nulls++
		} else {
			valids++
		}
	}
	if nulls != 1 || valids != 1 {
		t.Fatalf("items = %+v; want exactly one null and one valid", res.Items)
	}
}

func TestRun_ChildFailureNullsItemOnly(t *testing.T) {
	e, _, ctx := newEngine(t, func(context.Context) kernel.Agent {
		return &scriptChild{fail: true}
	}, Config{})
	spec := parse(t, e, `{"meta": {"name": "n", "description": "d"}, "items": ["x"],
	         "stages": [{"title": "s", "prompt": "p"}]}`)
	res, err := e.Run(ctx, spec)
	if err != nil {
		t.Fatalf("a child failure is an ordinary outcome, got %v", err)
	}
	if res.Items[0].Values[0] != nil {
		t.Fatalf("failed item must be null, got %v", res.Items[0].Values[0])
	}
}

func TestRun_PipelineSkipsRemainingStagesAfterNull(t *testing.T) {
	var calls int64
	e, _, ctx := newEngine(t, func(context.Context) kernel.Agent {
		atomic.AddInt64(&calls, 1)
		return &scriptChild{content: `{"step":1}`}
	}, Config{})
	// One item, two stages: the first stage's schema demands a field the
	// child never provides, so stage 2 must never run for that item.
	spec := parse(t, e, `{"meta": {"name": "n", "description": "d"}, "items": ["x"],
	         "stages": [
	           {"title": "s1", "prompt": "p1", "schema": {"type": "object", "required": ["missing"]}},
	           {"title": "s2", "prompt": "p2"}
	         ]}`)
	res, err := e.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("child calls = %d, want 1 (stage 2 must be skipped)", got)
	}
	if len(res.Items[0].Values) != 2 || res.Items[0].Values[1] != nil {
		t.Fatalf("skipped stages must stay null: %+v", res.Items[0])
	}
}

func TestRun_FatalFactoryNil(t *testing.T) {
	e, _, ctx := newEngine(t, func(context.Context) kernel.Agent { return nil }, Config{})
	spec := parse(t, e, validSpec)
	if _, err := e.Run(ctx, spec); !IsErrorCode(err, CodeAgentStart) {
		t.Fatalf("nil factory child must be CodeAgentStart, got %v", err)
	}
}

func TestRun_Canceled(t *testing.T) {
	release := make(chan struct{})
	e, _, ctx := newEngine(t, func(context.Context) kernel.Agent {
		return &scriptChild{content: "x", block: release}
	}, Config{MaxConcurrent: 1})
	spec := parse(t, e, validSpec)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := e.Run(runCtx, spec)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the first child start
	cancel()
	select {
	case err := <-done:
		if !IsErrorCode(err, CodeCanceled) {
			t.Fatalf("canceled run = %v, want CodeCanceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
	close(release)
}

func TestRun_RespectsMaxConcurrent(t *testing.T) {
	release := make(chan struct{})
	child := &scriptChild{content: "x", block: release}
	e, _, ctx := newEngine(t, func(context.Context) kernel.Agent { return child }, Config{MaxConcurrent: 3})
	spec := parse(t, e, `{"meta": {"name": "n", "description": "d"},
	         "items": [1,2,3,4,5,6,7,8,9,10],
	         "stages": [{"title": "s", "prompt": "p"}]}`)
	done := make(chan *Result, 1)
	go func() {
		res, err := e.Run(ctx, spec)
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- res
	}()
	// Wait until the semaphore is saturated, then give it time to try more.
	deadline := time.After(5 * time.Second)
	for {
		if child.active.Load() == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("concurrency never reached 3 (active=%d)", child.active.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(100 * time.Millisecond)
	if got := child.peak.Load(); got > 3 {
		t.Fatalf("peak concurrency %d exceeds MaxConcurrent 3", got)
	}
	close(release)
	<-done
}

func boolPtr(b bool) *bool { return &b }

// ensure the JSON round-trip shape stays stable for the model tool.
func TestResult_Marshals(t *testing.T) {
	res := Result{RunID: "wf-1", AgentsStarted: 2, Items: []ItemResult{{Index: 0, Values: []any{"x", nil}}}}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(b) {
		t.Fatal("result must be valid JSON")
	}
}
