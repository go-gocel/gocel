package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// 鈹€鈹€ Helpers 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

type testAgent struct {
	name         string
	desc         string
	sysPrompt    string
	maxSteps     int
	inputSchema  map[string]any
	outputSchema map[string]any
}

func (a *testAgent) Name() string                 { return a.name }
func (a *testAgent) Description() string          { return a.desc }
func (a *testAgent) InputSchema() map[string]any  { return a.inputSchema }
func (a *testAgent) OutputSchema() map[string]any { return a.outputSchema }

func (a *testAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	return &kernel.Result{Content: "test result"}
}

func (ag *testAgent) setName(v string)                 { ag.name = v }
func (ag *testAgent) setDescription(v string)          { ag.desc = v }
func (ag *testAgent) setSystemPrompt(v string)         { ag.sysPrompt = v }
func (ag *testAgent) setMaxSteps(v int)                { ag.maxSteps = v }
func (ag *testAgent) setInputSchema(v map[string]any)  { ag.inputSchema = v }
func (ag *testAgent) setOutputSchema(v map[string]any) { ag.outputSchema = v }

type testToolSource struct {
	name  string
	tools []kernel.Tool
}

func (s *testToolSource) Name() string                                       { return s.name }
func (s *testToolSource) ListTools(_ context.Context) ([]kernel.Tool, error) { return s.tools, nil }

type simpleTool struct {
	name string
	desc string
}

func (t *simpleTool) Name() string                                    { return t.name }
func (t *simpleTool) Description() string                             { return t.desc }
func (t *simpleTool) Schema() map[string]any                          { return map[string]any{"type": "object"} }
func (t *simpleTool) Run(_ context.Context, _ string) (string, error) { return "ok", nil }
func (t *simpleTool) ToolMeta() kernel.ToolMeta                       { return kernel.ToolMeta{} }
func (t *simpleTool) ListTools(_ context.Context) ([]kernel.Tool, error) {
	return []kernel.Tool{t}, nil
}

func makeTestMessages(totalChars int) []*types.Message {
	msgs := make([]*types.Message, 0, 3)
	msgs = append(msgs, types.NewSystemMessage("You are a helpful assistant."))
	content := strings.Repeat("x", totalChars)
	msgs = append(msgs, types.NewUserMessage(content))
	msgs = append(msgs, types.NewAssistantMessage("response."))
	return msgs
}

// 鈹€鈹€ Agent Interface & Options 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestAgentInterface(t *testing.T) {
	a := &testAgent{name: "test", desc: "test agent"}
	if a.Name() != "test" {
		t.Fatalf("Name = %q", a.Name())
	}
	if a.Description() != "test agent" {
		t.Fatalf("Description = %q", a.Description())
	}
	if a.InputSchema() != nil {
		t.Fatal("InputSchema should be nil")
	}
	if a.OutputSchema() != nil {
		t.Fatal("OutputSchema should be nil")
	}
	ctx := context.Background()
	result := a.Run(ctx, &types.AgentInput{}, nil)
	if result == nil {
		t.Fatal("Run returned nil")
	}
	if result.Content != "test result" {
		t.Fatalf("expected content 'test result', got %q", result.Content)
	}
	var _ kernel.Agent = a
}

type nameMock struct{ val string }

func (m *nameMock) setName(v string) { m.val = v }

type descMock struct{ val string }

func (m *descMock) setDescription(v string) { m.val = v }

type sysPromptMock struct{ val string }

func (m *sysPromptMock) setSystemPrompt(v string) { m.val = v }

type maxStepsMock struct{ val int }

func (m *maxStepsMock) setMaxSteps(v int) { m.val = v }

type schemaMock struct{ val map[string]any }

func (m *schemaMock) setInputSchema(v map[string]any)  { m.val = v }
func (m *schemaMock) setOutputSchema(v map[string]any) { m.val = v }

func TestAgentOptions(t *testing.T) {
	t.Run("WithName", func(t *testing.T) {
		m := &nameMock{}
		var v any = m
		if s, ok := v.(interface{ setName(string) }); ok {
			s.setName("agent1")
		}
		if m.val != "agent1" {
			t.Fatalf("name = %q (direct assertion)", m.val)
		}
	})

	t.Run("WithDescription", func(t *testing.T) {
		m := &descMock{}
		var v any = m
		if s, ok := v.(interface{ setDescription(string) }); ok {
			s.setDescription("desc1")
		}
		if m.val != "desc1" {
			t.Fatalf("desc = %q", m.val)
		}
	})

	t.Run("WithSystemPrompt", func(t *testing.T) {
		m := &sysPromptMock{}
		var v any = m
		if s, ok := v.(interface{ setSystemPrompt(string) }); ok {
			s.setSystemPrompt("prompt1")
		}
		if m.val != "prompt1" {
			t.Fatalf("sysPrompt = %q", m.val)
		}
	})

	t.Run("WithMaxSteps", func(t *testing.T) {
		m := &maxStepsMock{}
		var v any = m
		if s, ok := v.(interface{ setMaxSteps(int) }); ok {
			s.setMaxSteps(42)
		}
		if m.val != 42 {
			t.Fatalf("maxSteps = %d", m.val)
		}
	})

	t.Run("WithInputSchema", func(t *testing.T) {
		m := &schemaMock{}
		var v any = m
		schema := map[string]any{"type": "object"}
		if s, ok := v.(interface{ setInputSchema(map[string]any) }); ok {
			s.setInputSchema(schema)
		}
		if m.val == nil || m.val["type"] != "object" {
			t.Fatal("input schema not set")
		}
	})

	t.Run("WithOutputSchema", func(t *testing.T) {
		m := &schemaMock{}
		var v any = m
		schema := map[string]any{"type": "object"}
		if s, ok := v.(interface{ setOutputSchema(map[string]any) }); ok {
			s.setOutputSchema(schema)
		}
		if m.val == nil || m.val["type"] != "object" {
			t.Fatal("output schema not set")
		}
	})
}

// 鈹€鈹€ Tool Interface & Types 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestToolConstants(t *testing.T) {
	if kernel.ToolKindFunction != "function" {
		t.Fatalf("ToolKindFunction = %q", kernel.ToolKindFunction)
	}
	if kernel.ToolKindMCP != "mcp" {
		t.Fatalf("ToolKindMCP = %q", kernel.ToolKindMCP)
	}
	if kernel.ToolKindSkill != "skill" {
		t.Fatalf("ToolKindSkill = %q", kernel.ToolKindSkill)
	}
	if kernel.ToolKindBuiltin != "builtin" {
		t.Fatalf("ToolKindBuiltin = %q", kernel.ToolKindBuiltin)
	}
	if kernel.ToolKindAgent != "agent" {
		t.Fatalf("ToolKindAgent = %q", kernel.ToolKindAgent)
	}
}

func TestToolMetaStruct(t *testing.T) {
	tm := kernel.ToolMeta{
		Kind:    kernel.ToolKindFunction,
		Source:  "test",
		Tags:    []string{"a", "b"},
		Version: "1.0",
	}
	if tm.Kind != kernel.ToolKindFunction {
		t.Fatal("Kind mismatch")
	}
	if tm.Source != "test" {
		t.Fatal("Source mismatch")
	}
	if len(tm.Tags) != 2 {
		t.Fatal("Tags mismatch")
	}
	if tm.Version != "1.0" {
		t.Fatal("Version mismatch")
	}
}

func TestToolFromInfo(t *testing.T) {
	tl := &simpleTool{name: "my_tool", desc: "my description"}
	info := kernel.ToolFromInfo(tl)
	if info.Name != "my_tool" {
		t.Fatalf("Name = %q", info.Name)
	}
	if info.Description != "my description" {
		t.Fatalf("Description = %q", info.Description)
	}
	if info.Parameters == nil {
		t.Fatal("Parameters should not be nil")
	}
	// simpleTool.ToolMeta() returns zero value, so Kind is empty
	if info.Kind != "" {
		t.Fatalf("Kind = %q, want empty", info.Kind)
	}
}

func TestToolFromInfoWithMeta(t *testing.T) {
	tl := tool.NewSimpleFuncTool("meta_tool", "has meta", nil, func(ctx context.Context, s string) (string, error) {
		return "ok", nil
	}, tool.WithSimpleToolKind(kernel.ToolKindBuiltin))
	info := kernel.ToolFromInfo(tl)
	if info.Kind != kernel.ToolKindBuiltin {
		t.Fatalf("Kind = %q", info.Kind)
	}
}

func TestToolFromTools(t *testing.T) {
	tools := []kernel.Tool{
		&simpleTool{name: "a", desc: "tool a"},
		&simpleTool{name: "b", desc: "tool b"},
	}
	infos := kernel.ToolFromTools(tools)
	if len(infos) != 2 {
		t.Fatalf("len = %d", len(infos))
	}
	if infos[0].Name != "a" || infos[1].Name != "b" {
		t.Fatal("order/content mismatch")
	}
}

func TestNormalizeToolCall(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		tc := &types.ToolCall{}
		kernel.NormalizeToolCall(tc)
		if tc.Type != "function" {
			t.Fatalf("Type = %q", tc.Type)
		}
		if tc.ID == "" {
			t.Fatal("ID should not be empty")
		}
		if !strings.HasPrefix(tc.ID, "call_") {
			t.Fatalf("ID = %q, want call_ prefix", tc.ID)
		}
		if tc.Function.Arguments != "{}" {
			t.Fatalf("Arguments = %q", tc.Function.Arguments)
		}
	})

	t.Run("preserves existing values", func(t *testing.T) {
		tc := &types.ToolCall{
			ID:   "custom_id",
			Type: "custom_type",
			Function: types.ToolCallFunction{
				Arguments: `{"key":"val"}`,
			},
		}
		kernel.NormalizeToolCall(tc)
		if tc.ID != "custom_id" {
			t.Fatalf("ID changed to %q", tc.ID)
		}
		if tc.Type != "custom_type" {
			t.Fatalf("Type changed to %q", tc.Type)
		}
		if tc.Function.Arguments != `{"key":"val"}` {
			t.Fatalf("Arguments changed to %q", tc.Function.Arguments)
		}
	})

	t.Run("function name preserved", func(t *testing.T) {
		tc := &types.ToolCall{
			Function: types.ToolCallFunction{
				Name: "my_func",
			},
		}
		kernel.NormalizeToolCall(tc)
		if tc.Function.Name != "my_func" {
			t.Fatalf("Function.Name = %q", tc.Function.Name)
		}
	})
}

func TestWithToolCallKind(t *testing.T) {
	tc := &types.ToolCall{}
	opt := kernel.WithToolCallKind("mcp")
	opt(tc)
	if tc.Type != "mcp" {
		t.Fatalf("Type = %q", tc.Type)
	}
}

// 鈹€鈹€ MapToolRegistry 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestNewMapToolRegistry(t *testing.T) {
	tools := []kernel.Tool{&simpleTool{name: "a"}, &simpleTool{name: "b"}}
	r := tool.NewMapToolRegistry(tools)
	if r == nil {
		t.Fatal("registry is nil")
	}
	ctx := context.Background()
	list := r.List(ctx)
	if len(list) != 2 {
		t.Fatalf("List returned %d tools", len(list))
	}
}

func TestMapToolRegistry_List_CachedAndCopy(t *testing.T) {
	tools := []kernel.Tool{&simpleTool{name: "a"}}
	r := tool.NewMapToolRegistry(tools)
	ctx := context.Background()

	list1 := r.List(ctx)
	list2 := r.List(ctx)

	// Should return different slices (copies)
	if len(list1) != len(list2) {
		t.Fatal("length mismatch")
	}
	if &list1[0] == &list2[0] {
		t.Fatal("List should return copies")
	}
}

func TestMapToolRegistry_Get(t *testing.T) {
	tools := []kernel.Tool{&simpleTool{name: "alpha"}, &simpleTool{name: "beta"}}
	r := tool.NewMapToolRegistry(tools)
	ctx := context.Background()

	t.Run("existing", func(t *testing.T) {
		tl := r.Get(ctx, "alpha")
		if tl == nil {
			t.Fatal("expected non-nil")
		}
		if tl.Name() != "alpha" {
			t.Fatalf("name = %q", tl.Name())
		}
	})

	t.Run("unknown", func(t *testing.T) {
		tl := r.Get(ctx, "nonexistent")
		if tl != nil {
			t.Fatal("expected nil for unknown")
		}
	})
}

func TestMapToolRegistry_RegisterSource(t *testing.T) {
	tools := []kernel.Tool{&simpleTool{name: "base"}}
	r := tool.NewMapToolRegistry(tools)
	ctx := context.Background()
	rt := runtime.NewRuntime(nil, r)
	src := &testToolSource{name: "ext", tools: []kernel.Tool{&simpleTool{name: "ext_tool"}}}
	if err := rt.Register(ctx, src); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	list := r.List(ctx)
	if len(list) != 2 {
		t.Fatalf("List returned %d tools after Register, expected 2", len(list))
	}
	// Single-tool source where provider name differs → tool is prefixed: "ext/ext_tool"
	expectedName := "ext/ext_tool"
	found := false
	for _, tl := range list {
		if tl.Name() == expectedName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("tool %q not found in registry after Register", expectedName)
	}

	// Get from source
	tl := r.Get(ctx, expectedName)
	if tl == nil {
		t.Fatal("expected to find ext_tool via source")
	}
}

func TestMapToolRegistry_RegisterSourceInvalidatesCache(t *testing.T) {
	tools := []kernel.Tool{&simpleTool{name: "a"}}
	r := tool.NewMapToolRegistry(tools)
	ctx := context.Background()
	rt := runtime.NewRuntime(nil, r)

	r.List(ctx) // populate cache
	if err := rt.Register(ctx, &testToolSource{name: "s", tools: []kernel.Tool{&simpleTool{name: "b"}}}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	list := r.List(ctx)
	if len(list) != 2 {
		t.Fatalf("expected 2 after Register, got %d. Expected tools: 'a' and 's/b'", len(list))
	}
}

func TestMapToolRegistry_ThreadSafety(t *testing.T) {
	r := tool.NewMapToolRegistry([]kernel.Tool{&simpleTool{name: "safe"}})
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.List(ctx)
			r.Get(ctx, "safe")
		}()
	}
	wg.Wait()
}

func TestMapToolRegistry_List_Sorted(t *testing.T) {
	tools := []kernel.Tool{
		&simpleTool{name: "zebra"},
		&simpleTool{name: "apple"},
		&simpleTool{name: "mango"},
	}
	r := tool.NewMapToolRegistry(tools)
	ctx := context.Background()

	checkSorted := func(list []kernel.Tool) {
		t.Helper()
		for i := 1; i < len(list); i++ {
			if list[i-1].Name() > list[i].Name() {
				t.Fatalf("tools not sorted at %d: %q > %q", i, list[i-1].Name(), list[i].Name())
			}
		}
	}

	// First build (cache miss): must come back name-ascending.
	checkSorted(r.List(ctx))

	// Invalidate the cache and rebuild: the order must stay deterministic.
	if err := r.Add(ctx, &simpleTool{name: "banana"}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	checkSorted(r.List(ctx))
}

// 鈹€鈹€ Runtime AddTool / RemoveTool 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestRuntime_Register(t *testing.T) {
	reg := tool.NewMapToolRegistry([]kernel.Tool{&simpleTool{name: "base"}})
	rt := runtime.NewRuntime(nil, reg)
	ctx := context.Background()

	// Add a new tool
	err := rt.Register(ctx, &simpleTool{name: "added"})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	tools := rt.ListTools(ctx)
	found := false
	for _, tl := range tools {
		if tl.Name() == "added" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("added tool not found after Register")
	}

	// Add duplicate returns error
	err = rt.Register(ctx, &simpleTool{name: "base"})
	if err == nil {
		t.Fatal("expected error for duplicate Register")
	}
}

func TestRuntime_Unregister(t *testing.T) {
	reg := tool.NewMapToolRegistry([]kernel.Tool{
		&simpleTool{name: "keep"},
		&simpleTool{name: "remove_me"},
	})
	rt := runtime.NewRuntime(nil, reg)
	ctx := context.Background()

	// Remove one tool
	err := rt.Unregister(ctx, "remove_me")
	if err != nil {
		t.Fatalf("RemoveTool failed: %v", err)
	}

	tools := rt.ListTools(ctx)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after remove, got %d", len(tools))
	}
	if tools[0].Name() != "keep" {
		t.Fatalf("unexpected tool: %s", tools[0].Name())
	}

	// Remove unknown returns error
	err = rt.Unregister(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for removing nonexistent tool")
	}
}

func TestRuntime_Register_NilRegistry(t *testing.T) {
	rt := runtime.NewRuntime(nil, nil)
	ctx := context.Background()

	err := rt.Register(ctx, &simpleTool{name: "x"})
	if err != kernel.ErrNilTools {
		t.Fatalf("expected ErrNilTools, got %v", err)
	}

	err = rt.Unregister(ctx, "x")
	if err != kernel.ErrNilTools {
		t.Fatalf("expected ErrNilTools, got %v", err)
	}
}

func TestRuntime_Register_AffectsCallModel(t *testing.T) {
	// Verify that tools added via Register are passed to the model on the next CallModel.
	reg := tool.NewMapToolRegistry([]kernel.Tool{&simpleTool{name: "initial"}})
	callCount := 0
	var lastToolNames []string
	mockModel := &mockGenerateModel{
		generateFn: func(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
			callCount++
			// Apply all opts to GenConfig to inspect Tools field
			cfg := &kernel.GenConfig{}
			for _, opt := range opts {
				opt(cfg)
			}
			lastToolNames = nil
			for _, info := range cfg.Tools {
				lastToolNames = append(lastToolNames, info.Name)
			}
			return types.NewAssistantMessage("done"), &types.TokenUsage{}, nil
		},
	}

	rt := runtime.NewRuntime(mockModel, reg)
	ctx := context.Background()

	// First CallModel — only initial tool
	_, _, _ = rt.CallModel(ctx, []*types.Message{types.NewUserMessage("hi")})
	if callCount != 1 {
		t.Fatalf("expected 1 call, got %d", callCount)
	}
	if len(lastToolNames) != 1 || lastToolNames[0] != "initial" {
		t.Fatalf("expected [initial], got %v", lastToolNames)
	}

	// Add a tool mid-execution
	_ = rt.Register(ctx, &simpleTool{name: "added"})

	// Second CallModel — should include both
	_, _, _ = rt.CallModel(ctx, []*types.Message{types.NewUserMessage("hi again")})
	if callCount != 2 {
		t.Fatalf("expected 2 calls, got %d", callCount)
	}
	if len(lastToolNames) != 2 {
		t.Fatalf("expected 2 tools after add, got %v", lastToolNames)
	}

	// Remove and verify
	_ = rt.Unregister(ctx, "added")
	_, _, _ = rt.CallModel(ctx, []*types.Message{types.NewUserMessage("hi again")})
	if callCount != 3 {
		t.Fatalf("expected 3 calls, got %d", callCount)
	}
	if len(lastToolNames) != 1 || lastToolNames[0] != "initial" {
		t.Fatalf("expected [initial] after remove, got %v", lastToolNames)
	}
}

// 鈹€鈹€ SimpleFuncTool 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestNewFuncTool(t *testing.T) {
	fn := func(ctx context.Context, args string) (string, error) {
		return "hello", nil
	}
	tool := tool.NewSimpleFuncTool("greet", "Says hello", map[string]any{"type": "object"}, fn)
	if tool.Name() != "greet" {
		t.Fatalf("Name = %q", tool.Name())
	}
	if tool.Description() != "Says hello" {
		t.Fatalf("Description = %q", tool.Description())
	}
	schema := tool.Schema()
	if schema["type"] != "object" {
		t.Fatalf("Schema type = %v", schema["type"])
	}
	result, err := tool.Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result != "hello" {
		t.Fatalf("result = %q", result)
	}
}

func TestNewFuncToolOptions(t *testing.T) {
	t.Run("WithSimpleToolKind", func(t *testing.T) {
		fn := func(ctx context.Context, args string) (string, error) { return "", nil }
		tool := tool.NewSimpleFuncTool("k", "d", nil, fn, tool.WithSimpleToolKind(kernel.ToolKindBuiltin))
		tm := tool.ToolMeta()
		if tm.Kind != kernel.ToolKindBuiltin {
			t.Fatalf("Kind = %v", tm.Kind)
		}
	})

	t.Run("WithSimpleToolSource", func(t *testing.T) {
		fn := func(ctx context.Context, args string) (string, error) { return "", nil }
		tool := tool.NewSimpleFuncTool("k", "d", nil, fn, tool.WithSimpleToolSource("builtin:test"))
		tm := tool.ToolMeta()
		if tm.Source != "builtin:test" {
			t.Fatalf("Source = %q", tm.Source)
		}
	})

	t.Run("both options", func(t *testing.T) {
		fn := func(ctx context.Context, args string) (string, error) { return "", nil }
		tool := tool.NewSimpleFuncTool("k", "d", nil, fn,
			tool.WithSimpleToolKind(kernel.ToolKindFunction),
			tool.WithSimpleToolSource("test:src"),
		)
		tm := tool.ToolMeta()
		if tm.Kind != kernel.ToolKindFunction || tm.Source != "test:src" {
			t.Fatal("options not applied")
		}
	})
}

func TestSimpleFuncTool_ToolMeta(t *testing.T) {
	fn := func(ctx context.Context, args string) (string, error) { return "", nil }
	tool := tool.NewSimpleFuncTool("meta", "desc", nil, fn)
	tm := tool.ToolMeta()
	// Default zero values
	if tm.Kind != "" {
		t.Fatalf("Kind = %v", tm.Kind)
	}
	if tm.Source != "" {
		t.Fatalf("Source = %q", tm.Source)
	}
}

func TestSimpleFuncTool_ToolInterface(t *testing.T) {
	fn := func(ctx context.Context, args string) (string, error) { return "ok", nil }
	tool := tool.NewSimpleFuncTool("iface", "check", nil, fn)
	var _ kernel.Tool = tool
}

// 鈹€鈹€ ChatModel & GenOption 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestGenConfig(t *testing.T) {
	cfg := &kernel.GenConfig{}
	if cfg.Tools != nil {
		t.Fatal("Tools should be nil by default")
	}
	if cfg.Temperature != 0 {
		t.Fatal("Temperature should be 0")
	}
	if cfg.MaxTokens != 0 {
		t.Fatal("MaxTokens should be 0")
	}
}

func TestGenConfig_Apply(t *testing.T) {
	cfg := &kernel.GenConfig{}
	opts := []kernel.GenOption{
		kernel.WithTools([]*kernel.ToolInfo{{Name: "t1"}}),
		kernel.WithTemperature(0.7),
		kernel.WithMaxTokens(100),
		kernel.WithTopP(0.9),
		kernel.WithStop([]string{"\n"}),
	}
	cfg.Apply(opts)
	if len(cfg.Tools) != 1 || cfg.Tools[0].Name != "t1" {
		t.Fatal("Tools not applied")
	}
	if cfg.Temperature != 0.7 {
		t.Fatalf("Temperature = %f", cfg.Temperature)
	}
	if cfg.MaxTokens != 100 {
		t.Fatalf("MaxTokens = %d", cfg.MaxTokens)
	}
	if cfg.TopP != 0.9 {
		t.Fatalf("TopP = %f", cfg.TopP)
	}
	if len(cfg.Stop) != 1 || cfg.Stop[0] != "\n" {
		t.Fatal("Stop not applied")
	}
}

func TestGenOptions_Individually(t *testing.T) {
	t.Run("WithTools", func(t *testing.T) {
		cfg := &kernel.GenConfig{}
		kernel.WithTools([]*kernel.ToolInfo{{Name: "a"}})(cfg)
		if len(cfg.Tools) != 1 {
			t.Fatal("WithTools failed")
		}
	})

	t.Run("WithTemperature", func(t *testing.T) {
		cfg := &kernel.GenConfig{}
		kernel.WithTemperature(0.5)(cfg)
		if cfg.Temperature != 0.5 {
			t.Fatal("WithTemperature failed")
		}
	})

	t.Run("WithMaxTokens", func(t *testing.T) {
		cfg := &kernel.GenConfig{}
		kernel.WithMaxTokens(200)(cfg)
		if cfg.MaxTokens != 200 {
			t.Fatal("WithMaxTokens failed")
		}
	})

	t.Run("WithTopP", func(t *testing.T) {
		cfg := &kernel.GenConfig{}
		kernel.WithTopP(0.8)(cfg)
		if cfg.TopP != 0.8 {
			t.Fatal("WithTopP failed")
		}
	})

	t.Run("WithStop", func(t *testing.T) {
		cfg := &kernel.GenConfig{}
		kernel.WithStop([]string{"stop1"})(cfg)
		if len(cfg.Stop) != 1 || cfg.Stop[0] != "stop1" {
			t.Fatal("WithStop failed")
		}
	})
}

// 鈹€鈹€ StreamReader 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestStreamReaderInterface(t *testing.T) {
	var _ kernel.StreamReader = &mockStreamReader{}
}

type mockGenerateModel struct {
	generateFn func(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error)
}

func (m *mockGenerateModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, msgs, opts...)
	}
	return types.NewAssistantMessage("mock"), &types.TokenUsage{}, nil
}

func (m *mockGenerateModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return &mockStreamReader{}, nil
}

func (m *mockGenerateModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return 0, nil
}

type mockStreamReader struct{}

func (m *mockStreamReader) Recv() (*types.Message, error) { return nil, errors.New("EOF") }
func (m *mockStreamReader) Close() error                  { return nil }
func (m *mockStreamReader) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

type testJSONStruct struct {
	Name  string  `json:"name"`
	Age   int     `json:"age"`
	Score float64 `json:"score"`
}

func TestResponseFormatJSON(t *testing.T) {
	rf := kernel.ResponseFormatJSON()
	if rf.Type != "json_object" {
		t.Fatalf("Type = %q, want json_object", rf.Type)
	}
	if rf.Schema != nil {
		t.Fatal("Schema should be nil for json_object")
	}
}

func TestResponseFormatJSONSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	rf := kernel.ResponseFormatJSONSchema(schema)
	if rf.Type != "json_schema" {
		t.Fatalf("Type = %q, want json_schema", rf.Type)
	}
	if rf.Schema == nil {
		t.Fatal("Schema should not be nil")
	}
	if string(rf.Schema) != string(schema) {
		t.Fatalf("Schema = %s, want %s", string(rf.Schema), string(schema))
	}
}

func TestWithResponseFormat(t *testing.T) {
	cfg := &kernel.GenConfig{}
	rf := kernel.ResponseFormatJSON()
	kernel.WithResponseFormat(rf)(cfg)
	if cfg.ResponseFormat == nil {
		t.Fatal("ResponseFormat should be set")
	}
	if cfg.ResponseFormat.Type != "json_object" {
		t.Fatalf("Type = %q", cfg.ResponseFormat.Type)
	}
}

func TestWithResponseFormatJSONSchema(t *testing.T) {
	cfg := &kernel.GenConfig{}
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"number"}}}`)
	kernel.WithResponseFormat(kernel.ResponseFormatJSONSchema(schema))(cfg)
	if cfg.ResponseFormat == nil {
		t.Fatal("ResponseFormat should be set")
	}
	if cfg.ResponseFormat.Type != "json_schema" {
		t.Fatalf("Type = %q", cfg.ResponseFormat.Type)
	}
	if cfg.ResponseFormat.Schema == nil {
		t.Fatal("Schema should be set")
	}
}

func TestParseAs(t *testing.T) {
	t.Run("valid struct", func(t *testing.T) {
		content := `{"name":"Alice","age":30,"score":95.5}`
		var target testJSONStruct
		if err := kernel.ParseAs(content, &target); err != nil {
			t.Fatalf("ParseAs: %v", err)
		}
		if target.Name != "Alice" || target.Age != 30 || target.Score != 95.5 {
			t.Fatalf("got %+v", target)
		}
	})

	t.Run("nil target", func(t *testing.T) {
		err := kernel.ParseAs("{}", (*testJSONStruct)(nil))
		if err == nil {
			t.Fatal("expected error for nil target")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		var target testJSONStruct
		err := kernel.ParseAs("", &target)
		if err == nil {
			t.Fatal("expected error for empty content")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		var target testJSONStruct
		err := kernel.ParseAs("not json", &target)
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("generic map target", func(t *testing.T) {
		content := `{"key":"value"}`
		var target map[string]any
		if err := kernel.ParseAs(content, &target); err != nil {
			t.Fatalf("ParseAs: %v", err)
		}
		if target["key"] != "value" {
			t.Fatalf("got %v", target)
		}
	})
}

func TestValidateSchema(t *testing.T) {
	t.Run("valid JSON, no schema", func(t *testing.T) {
		err := kernel.ValidateSchema(`{"a":1}`, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		err := kernel.ValidateSchema("not json", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		err := kernel.ValidateSchema("", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("json_object type skips schema check", func(t *testing.T) {
		rf := kernel.ResponseFormatJSON()
		err := kernel.ValidateSchema(`{"a":1}`, &rf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("json_schema type validation passes", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"number"}}}`)
		rf := kernel.ResponseFormatJSONSchema(schema)
		err := kernel.ValidateSchema(`{"x":42}`, &rf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// errModel returns an error from Generate — for testing retry/error paths.
type errModel struct{}

func (m *errModel) Generate(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return nil, nil, errors.New("model error")
}

func (m *errModel) Stream(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}

func (m *errModel) CountTokens(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (int, error) {
	return 0, nil
}

func TestExecTools_HookBeforeToolCall_Blocks(t *testing.T) {
	reg := tool.NewMapToolRegistry([]kernel.Tool{
		&simpleTool{name: "test_tool", desc: "a test tool"},
	})
	rt := runtime.NewRuntime(nil, reg)

	rt.OnToolCall(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		return ctx, info, errors.New("blocked by test")
	})

	results := rt.ExecTools(context.Background(), []*types.ToolCall{
		{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "test_tool", Arguments: "{}"}},
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	msg := results[0]
	if msg == nil {
		t.Fatal("result[0] is nil")
	}
	if msg.Role != types.RoleTool {
		t.Fatalf("expected RoleTool, got %v", msg.Role)
	}
	if !strings.Contains(msg.Content, "blocked by test") {
		t.Fatalf("expected blocked error in content, got %q", msg.Content)
	}
	if msg.ToolCallID != "call_1" {
		t.Fatalf("expected ToolCallID 'call_1', got %q", msg.ToolCallID)
	}
	if msg.ToolName != "test_tool" {
		t.Fatalf("expected ToolName 'test_tool', got %q", msg.ToolName)
	}
}

func TestExecTools_HookAfterToolCall_Rejects(t *testing.T) {
	reg := tool.NewMapToolRegistry([]kernel.Tool{
		&simpleTool{name: "test_tool", desc: "a test tool"},
	})
	rt := runtime.NewRuntime(nil, reg)

	rt.OnToolResult(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		if info.Result == "ok" {
			return ctx, info, errors.New("content check failed")
		}
		return ctx, info, nil
	})

	results := rt.ExecTools(context.Background(), []*types.ToolCall{
		{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "test_tool", Arguments: "{}"}},
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	msg := results[0]
	if msg == nil {
		t.Fatal("result[0] is nil")
	}
	if msg.Role != types.RoleTool {
		t.Fatalf("expected RoleTool, got %v", msg.Role)
	}
	if !strings.Contains(msg.Content, "content check failed") {
		t.Fatalf("expected rejection error in content, got %q", msg.Content)
	}
	if msg.ToolCallID != "call_1" {
		t.Fatalf("expected ToolCallID 'call_1', got %q", msg.ToolCallID)
	}
	if msg.ToolName != "test_tool" {
		t.Fatalf("expected ToolName 'test_tool', got %q", msg.ToolName)
	}
}

func TestExecTools_HookBeforeToolCall_SomeBlocked(t *testing.T) {
	reg := tool.NewMapToolRegistry([]kernel.Tool{
		&simpleTool{name: "allowed", desc: ""},
		&simpleTool{name: "blocked1", desc: ""},
		&simpleTool{name: "blocked2", desc: ""},
	})
	rt := runtime.NewRuntime(nil, reg)

	rt.OnToolCall(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		if info.Name == "allowed" {
			return ctx, info, nil
		}
		return ctx, nil, errors.New("blocked: " + info.Name)
	})

	results := rt.ExecTools(context.Background(), []*types.ToolCall{
		{ID: "call_a", Type: "function", Function: types.ToolCallFunction{Name: "allowed", Arguments: "{}"}},
		{ID: "call_b", Type: "function", Function: types.ToolCallFunction{Name: "blocked1", Arguments: "{}"}},
		{ID: "call_c", Type: "function", Function: types.ToolCallFunction{Name: "blocked2", Arguments: "{}"}},
	})

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// allowed wasn't blocked — batch aborted, so it was skipped. It must
	// still get a tool message: providers reject assistant tool_calls that
	// are not answered by a tool message for every tool_call_id.
	if results[0] == nil {
		t.Fatal("expected non-nil for skipped tool (every tool_call needs a result)")
	}
	if !strings.Contains(results[0].Content, "tool call skipped") {
		t.Errorf("skipped content = %q", results[0].Content)
	}
	if results[0].ToolCallID != "call_a" {
		t.Errorf("skipped ToolCallID = %q", results[0].ToolCallID)
	}
	if results[0].ToolName != "allowed" {
		t.Errorf("skipped ToolName = %q", results[0].ToolName)
	}
	// blocked1
	if results[1] == nil {
		t.Fatal("expected non-nil for blocked1")
	}
	if !strings.Contains(results[1].Content, "blocked: blocked1") {
		t.Errorf("blocked1 content = %q", results[1].Content)
	}
	if results[1].ToolCallID != "call_b" {
		t.Errorf("blocked1 ToolCallID = %q", results[1].ToolCallID)
	}
	if results[1].ToolName != "blocked1" {
		t.Errorf("blocked1 ToolName = %q", results[1].ToolName)
	}
	// blocked2
	if results[2] == nil {
		t.Fatal("expected non-nil for blocked2")
	}
	if !strings.Contains(results[2].Content, "blocked: blocked2") {
		t.Errorf("blocked2 content = %q", results[2].Content)
	}
	if results[2].ToolCallID != "call_c" {
		t.Errorf("blocked2 ToolCallID = %q", results[2].ToolCallID)
	}
	if results[2].ToolName != "blocked2" {
		t.Errorf("blocked2 ToolName = %q", results[2].ToolName)
	}
}

func TestCallModel_HookAfterModelCall_ErrorAbortsRetry(t *testing.T) {
	rt := runtime.NewRuntime(&errModel{}, nil,
		runtime.WithRetryPolicy(runtime.RetryPolicy{
			MaxRetries:   3,
			BaseDelay:    0,
			MaxDelay:     0,
			RetryOnModel: true,
		}),
	)

	rt.OnModelResult(func(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
		if info.Error != nil {
			return ctx, info, errors.New("hook rejects model error")
		}
		return ctx, info, nil
	})

	_, _, err := rt.CallModel(context.Background(), []*types.Message{types.NewUserMessage("test")})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "hook rejects model error") {
		t.Fatalf("expected hook error in message, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "hook after model call") {
		t.Fatalf("expected 'hook after model call' prefix, got %q", err.Error())
	}
}
