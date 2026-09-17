// Package workflow exposes the spec-driven workflow engine to the model:
// one tool takes a spec document (identity meta + fan-out items + a stage
// pipeline), executes it over one-shot subagents, and returns the run id,
// the number of started agents, and the per-item results. Results are
// truncated at maxResultChars (DSH fixed cap) — the full detail belongs to
// the run's children, not the tool result.
//
// Package workflow 把 spec 驱动的工作流引擎暴露给模型：一个工具接收 spec
// 文档（身份 meta + 扇出条目 + 阶段管线），在一次性子代理上执行，返回
// run id、已启动代理数与逐条目结果。结果按 maxResultChars 截断（DSH 固定
// 上限）——完整细节属于各子代理，不属于工具结果。
package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"

	workflowengine "github.com/go-gocel/gocel/agents/workflow"
)

// maxResultChars bounds the tool result (DSH fixed cap).
const maxResultChars = 50000

// Config wires the tool to an engine.
//
// Config 把工具接到工作流引擎上。
type Config struct {
	// Engine is the workflow engine the tool drives.
	Engine *workflowengine.Engine
}

// Tools builds the workflow tool.
//
// Tools 构建工作流工具。
func Tools(cfg Config) ([]kernel.Tool, error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("workflow tool: nil engine")
	}
	run, err := tool.ToolFromFunc(
		func(ctx context.Context, args runArgs) (string, error) {
			return runWorkflow(ctx, cfg.Engine, args.Spec)
		},
		tool.WithToolName("workflow"),
		tool.WithToolDescription("Run a spec-driven fan-out workflow over background subagents. spec: JSON with meta{name,description,phases?}, items (fan-out inputs), stages[{title,prompt,schema?}] — {{item}}/{{index}} substitute into prompts, the optional schema validates each child's JSON result (subset: type/properties/required/additionalProperties/items/enum/const/oneOf). Items flow through stages independently; a child failure nulls only that item. Returns {run_id, agents_started, items}."),
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	return []kernel.Tool{run}, nil
}

// MustTools builds the tool set, panicking on configuration errors.
//
// MustTools 构建工具集，配置错误时直接 panic。
func MustTools(cfg Config) []kernel.Tool {
	ts, err := Tools(cfg)
	if err != nil {
		panic(err)
	}
	return ts
}

type runArgs struct {
	Spec string `json:"spec" description:"The workflow spec document (JSON)"`
}

// runOut is the fixed tool output shape (DSH: runId/agentsStarted/result).
type runOut struct {
	RunID         string                      `json:"run_id"`
	AgentsStarted int                         `json:"agents_started"`
	Items         []workflowengine.ItemResult `json:"items"`
}

func runWorkflow(ctx context.Context, e *workflowengine.Engine, raw string) (string, error) {
	spec, err := e.ParseSpec([]byte(raw))
	if err != nil {
		// Fatal vocabulary reaches the model verbatim — it must fix the spec.
		return "", err
	}
	res, err := e.Run(ctx, spec)
	if err != nil {
		return "", err
	}
	out := runOut{RunID: res.RunID, AgentsStarted: res.AgentsStarted, Items: res.Items}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("workflow: marshal result: %w", err)
	}
	if len(b) > maxResultChars {
		suffix := []byte(" ...[truncated]")
		b = append(b[:maxResultChars-len(suffix)], suffix...)
	}
	return string(b), nil
}
