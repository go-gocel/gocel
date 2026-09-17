// Package workflow executes declarative fan-out workflows over subagents:
// the model writes a spec (identity meta + items + a stage pipeline), and
// the engine runs every item through the stages on one-shot subagents with
// optional per-stage JSON-schema validation (DSH workflow semantics).
//
// Error model (DSH): configuration and protocol misuse are FATAL — an
// *Error with a closed code kills the whole workflow. An individual
// child's failure or schema mismatch is an ordinary outcome: that item
// degrades to null and skips its remaining stages.
//
// Package workflow 在子代理上执行声明式扇出工作流：模型写出 spec（身份
// meta + 条目 + 阶段管线），引擎用一次性子代理把每个条目跑过各阶段，
// 可选按阶段 JSON-schema 校验（DSH workflow 语义）。
//
// 错误模型（DSH）：配置与协议误用是致命的——带封闭码的 *Error 终止整个
// 工作流。单个子代理失败或 schema 不匹配是普通结果：该条目降级为 null
// 并跳过剩余阶段。
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
)

// ErrorCode is the closed workflow error vocabulary. Every code is fatal.
//
// ErrorCode 是封闭的工作流错误词汇。所有码都是致命的。
type ErrorCode string

const (
	// CodeInvalidSpec reports that the spec document is malformed or fails
	// validation.
	//
	// CodeInvalidSpec 表示 spec 文档格式错误或校验失败。
	CodeInvalidSpec ErrorCode = "invalid_spec"
	// CodeUnsupportedSchema reports that the schema uses keywords outside the
	// supported subset.
	//
	// CodeUnsupportedSchema 表示 schema 使用了支持子集之外的关键字。
	CodeUnsupportedSchema ErrorCode = "unsupported_schema"
	// CodeAgentCap reports that a configured cap would be exceeded.
	//
	// CodeAgentCap 表示将超过配置的上限。
	CodeAgentCap ErrorCode = "agent_cap"
	// CodeItemCap reports that the item count would exceed the configured cap.
	//
	// CodeItemCap 表示条目数将超过配置的上限。
	CodeItemCap  ErrorCode = "item_cap"
	// CodeAgentStart reports that a child could not be started at all.
	//
	// CodeAgentStart 表示子代理完全无法启动。
	CodeAgentStart ErrorCode = "agent_start"
	// CodeCanceled reports that the workflow context was canceled.
	//
	// CodeCanceled 表示工作流上下文被取消。
	CodeCanceled ErrorCode = "cancelled"
)

// Error is the fatal workflow error. Unwrap reaches the underlying cause.
//
// Error 是致命的工作流错误。Unwrap 可达底层原因。
type Error struct {
	Code ErrorCode
	Err  error
}

// Error returns the formatted workflow error message.
//
// Error 返回格式化的工作流错误信息。
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("workflow: %s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("workflow: %s", e.Code)
}

// Unwrap returns the underlying cause.
//
// Unwrap 返回底层原因。
func (e *Error) Unwrap() error { return e.Err }

func fatalf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...)}
}

// IsErrorCode reports whether err is (or wraps) an *Error with the code.
//
// IsErrorCode 判断 err 是否为（或包装了）带指定码的 *Error。
func IsErrorCode(err error, code ErrorCode) bool {
	var w *Error
	return errors.As(err, &w) && w.Code == code
}

// Meta is the workflow identity block: name and description are required
// (DSH), phases are optional progress labels.
//
// Meta 是工作流身份块：name 与 description 必填（DSH），phases 为可选进度标签。
type Meta struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Phases      []Phase `json:"phases,omitempty"`
}

// Phase labels one progress phase.
//
// Phase 标记一个进度阶段。
type Phase struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// Stage is one pipeline stage applied to every item. Prompt is a template:
// {{item}} is replaced by the item's JSON, {{index}} by its index.
//
// Stage 是应用到每个条目的一个管线阶段。Prompt 是模板：{{item}} 被替换为
// 条目的 JSON，{{index}} 被替换为其下标。
type Stage struct {
	Title  string  `json:"title"`
	Prompt string  `json:"prompt"`
	Schema *Schema `json:"schema,omitempty"`
}

// Spec is the declarative workflow document.
//
// Spec 是声明式工作流文档。
type Spec struct {
	Meta   Meta    `json:"meta"`
	Items  []any   `json:"items"`
	Stages []Stage `json:"stages"`
}

// ItemResult is one item's trace through the pipeline: Values[i] is the
// value of stage i (parsed JSON when the stage declares a schema, the raw
// child text otherwise), null where the item failed that stage (remaining
// stages are skipped).
//
// ItemResult 是单个条目在管线中的轨迹：Values[i] 是阶段 i 的值（阶段声明
// schema 时为解析后的 JSON，否则为子代理原始文本），条目在该阶段失败处为
// null（后续阶段被跳过）。
type ItemResult struct {
	Index  int   `json:"index"`
	Values []any `json:"values"`
}

// Result is the workflow outcome.
//
// Result 是工作流的输出结果。
type Result struct {
	RunID         string       `json:"run_id"`
	AgentsStarted int          `json:"agents_started"`
	Items         []ItemResult `json:"items"`
}

// Config wires the engine to the subagent mechanism.
//
// Config 把引擎接到子代理机制上。
type Config struct {
	// Registry runs the one-shot children (the parent runtime rides the
	// tool context, as with subagent tools).
	Registry *orchestrate.Registry
	// Factory builds a fresh child agent per stage-item (DSH one-shot
	// provider: spawn a new child per agent() call).
	Factory func(ctx context.Context) kernel.Agent
	// MaxConcurrent bounds simultaneously running children; default 16.
	MaxConcurrent int
	// MaxTotalAgents / MaxItemsPerCall bound one workflow run; defaults
	// 1000 / 4096 (DSH defaults).
	MaxTotalAgents  int
	MaxItemsPerCall int
}

// Engine executes specs.
//
// Engine 执行工作流 spec。
type Engine struct {
	cfg   Config
	slots *slotQueue
}

// New validates the configuration.
//
// New 校验配置（registry 与 factory 必填），填充默认上限并创建引擎。
func New(cfg Config) (*Engine, error) {
	if cfg.Registry == nil {
		return nil, errors.New("workflow: nil registry")
	}
	if cfg.Factory == nil {
		return nil, errors.New("workflow: nil factory")
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 16
	}
	if cfg.MaxTotalAgents == 0 {
		cfg.MaxTotalAgents = 1000
	}
	if cfg.MaxItemsPerCall == 0 {
		cfg.MaxItemsPerCall = 4096
	}
	return &Engine{cfg: cfg, slots: newSlotQueue(cfg.MaxConcurrent)}, nil
}

// rawStage keeps the schema as raw JSON so unsupported keywords survive
// decoding long enough to be rejected (a typed Schema decode would drop
// them silently).
type rawStage struct {
	Title  string          `json:"title"`
	Prompt string          `json:"prompt"`
	Schema json.RawMessage `json:"schema"`
}

// ParseSpec decodes and validates a spec document: meta fields required,
// at least one stage, supported schema keywords only, caps respected.
//
// ParseSpec 解码并校验 spec 文档：meta 必填、至少一个阶段、schema 仅限
// 支持的关键字、不超过上限。
func (e *Engine) ParseSpec(raw []byte) (*Spec, error) {
	var rawDoc struct {
		Meta   Meta       `json:"meta"`
		Items  []any      `json:"items"`
		Stages []rawStage `json:"stages"`
	}
	if err := json.Unmarshal(raw, &rawDoc); err != nil {
		return nil, fatalf(CodeInvalidSpec, "parse: %v", err)
	}
	if rawDoc.Meta.Name == "" || rawDoc.Meta.Description == "" {
		return nil, fatalf(CodeInvalidSpec, "meta.name and meta.description are required")
	}
	if len(rawDoc.Stages) == 0 {
		return nil, fatalf(CodeInvalidSpec, "at least one stage is required")
	}
	if len(rawDoc.Items) > e.cfg.MaxItemsPerCall {
		return nil, fatalf(CodeItemCap, "items %d exceeds max %d", len(rawDoc.Items), e.cfg.MaxItemsPerCall)
	}
	if len(rawDoc.Items)*len(rawDoc.Stages) > e.cfg.MaxTotalAgents {
		return nil, fatalf(CodeAgentCap, "stage-items %d exceeds max agents %d", len(rawDoc.Items)*len(rawDoc.Stages), e.cfg.MaxTotalAgents)
	}

	spec := &Spec{Meta: rawDoc.Meta, Items: rawDoc.Items, Stages: make([]Stage, len(rawDoc.Stages))}
	for i, st := range rawDoc.Stages {
		if st.Prompt == "" {
			return nil, fatalf(CodeInvalidSpec, "stage %d: prompt is required", i)
		}
		spec.Stages[i] = Stage{Title: st.Title, Prompt: st.Prompt}
		if len(st.Schema) == 0 {
			continue
		}
		if err := checkSchemaKeywords(st.Schema); err != nil {
			return nil, fatalf(CodeUnsupportedSchema, "stage %d: %v", i, err)
		}
		var schema Schema
		if err := json.Unmarshal(st.Schema, &schema); err != nil {
			return nil, fatalf(CodeInvalidSpec, "stage %d schema: %v", i, err)
		}
		spec.Stages[i].Schema = &schema
	}
	return spec, nil
}
