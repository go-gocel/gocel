package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
	"github.com/go-gocel/gocel/core/types"

	workflowengine "github.com/go-gocel/gocel/agents/workflow"
)

// stubRuntime satisfies kernel.Runtime for the engine's one-shot children.
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

type child struct{ content string }

func (c *child) Name() string        { return "child" }
func (c *child) Description() string { return "" }
func (c *child) Run(context.Context, *types.AgentInput, kernel.Runtime) *kernel.Result {
	return &kernel.Result{Content: c.content}
}

func newTool(t *testing.T) kernel.Tool {
	t.Helper()
	engine, err := workflowengine.New(workflowengine.Config{
		Registry: orchestrate.NewRegistry(),
		Factory:  func(context.Context) kernel.Agent { return &child{content: `{"ok":true}`} },
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	tools := MustTools(Config{Engine: engine})
	for _, tl := range tools {
		if tl.Name() == "workflow" {
			return tl
		}
	}
	t.Fatal("workflow tool not built")
	return nil
}

const spec = `{"meta":{"name":"audit","description":"audit files"},"items":["a.go","b.go"],"stages":[{"title":"s","prompt":"do {{item}}"}]}`

// TestWorkflowTool_RunsSpec proves the full tool path: parse → fan-out →
// fixed output shape {run_id, agents_started, items}.
func TestWorkflowTool_RunsSpec(t *testing.T) {
	tl := newTool(t)
	ctx := kernel.WithRuntime(context.Background(), &stubRuntime{})
	args, _ := json.Marshal(map[string]any{"spec": spec})
	out, err := tl.Run(ctx, string(args))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var parsed runOut
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid runOut JSON: %v", err)
	}
	if parsed.RunID == "" || parsed.AgentsStarted != 2 || len(parsed.Items) != 2 {
		t.Fatalf("output = %+v", parsed)
	}
	if parsed.Items[0].Values[0] != "{\"ok\":true}" {
		t.Fatalf("unschema'd stage value must be the raw text, got %v", parsed.Items[0].Values[0])
	}
}

// TestWorkflowTool_InvalidSpecSurfacesFatalVocab proves the closed error
// vocabulary reaches the model verbatim.
func TestWorkflowTool_InvalidSpecSurfacesFatalVocab(t *testing.T) {
	tl := newTool(t)
	ctx := kernel.WithRuntime(context.Background(), &stubRuntime{})
	_, err := tl.Run(ctx, `{"spec":"{\"meta\":{\"name\":\"n\"},\"items\":[],\"stages\":[{\"title\":\"s\",\"prompt\":\"p\"}]}"}`)
	if !workflowengine.IsErrorCode(err, workflowengine.CodeInvalidSpec) {
		t.Fatalf("invalid spec must surface CodeInvalidSpec, got %v", err)
	}
}

// TestWorkflowTool_MissingSpecArgRejected proves required-argument
// enforcement at the tool boundary.
func TestWorkflowTool_MissingSpecArgRejected(t *testing.T) {
	tl := newTool(t)
	if _, err := tl.Run(context.Background(), `{}`); err == nil {
		t.Fatal("missing spec must be rejected")
	}
}

// TestWorkflowTool_ResultTruncatedAtCap proves the DSH maxResultChars cap.
func TestWorkflowTool_ResultTruncatedAtCap(t *testing.T) {
	engine, err := workflowengine.New(workflowengine.Config{
		Registry: orchestrate.NewRegistry(),
		Factory: func(context.Context) kernel.Agent {
			return &child{content: strings.Repeat("x", maxResultChars+1000)}
		},
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	ctx := kernel.WithRuntime(context.Background(), &stubRuntime{})
	out, err := runWorkflow(ctx, engine, `{"meta":{"name":"n","description":"d"},"items":["big"],"stages":[{"title":"s","prompt":"p"}]}`)
	if err != nil {
		t.Fatalf("runWorkflow: %v", err)
	}
	if len(out) > maxResultChars {
		t.Fatalf("result %d bytes exceeds cap %d", len(out), maxResultChars)
	}
	if !strings.HasSuffix(out, "...[truncated]") {
		t.Fatal("truncated result must carry the marker")
	}
}

// TestWorkflowTool_ReadOnlyEffects proves the tool declares no mutating
// effects (children run under the parent's gates).
func TestWorkflowTool_ReadOnlyEffects(t *testing.T) {
	tl := newTool(t)
	for _, e := range kernel.EffectiveEffects(tl) {
		if e == kernel.EffectWrite || e == kernel.EffectExec || e == kernel.EffectUserData {
			t.Fatalf("workflow tool must not declare mutating effects, got %v", kernel.EffectiveEffects(tl))
		}
	}
}
