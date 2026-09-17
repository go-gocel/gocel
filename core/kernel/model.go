// package kernel defines all interfaces that gocel depends on.
// Implement these interfaces to integrate with any LLM provider or tool system.
package kernel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-gocel/gocel/core/types"
)

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// Model is the user-facing abstraction for a language model.
// It provides metadata and token counting without exposing Generate/Stream.
// Users pass Model to agent constructors and Runner — they never call Generate/Stream directly.
//
// Model 是用户可见的模型抽象，提供元数据和 token 计数，不暴露 Generate/Stream。
type Model interface {
	// CountTokens estimates the number of tokens in the given messages.
	// CountTokens 估算消息的 token 数量。
	CountTokens(ctx context.Context, messages []*types.Message, opts ...GenOption) (int, error)
}

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// ChatModel is the full provider contract for a language model.
// Implement this interface to integrate any LLM provider.
// Users should NOT call Generate/Stream directly — use Runtime.CallModel / Runtime.CallModelStream instead.
//
// ChatModel 是语言模型的完整提供者契约。实现此接口可接入任何 LLM 提供者。
// 用户不应直接调用 Generate/Stream，应使用 Runtime.CallModel / Runtime.CallModelStream。
type ChatModel interface {
	Model

	// Generate performs a synchronous chat completion.
	// Generate 执行同步聊天补全。
	Generate(ctx context.Context, messages []*types.Message, opts ...GenOption) (*types.Message, *types.TokenUsage, error)

	// Stream performs a streaming chat completion.
	// Stream 执行流式聊天补全。
	Stream(ctx context.Context, messages []*types.Message, opts ...GenOption) (StreamReader, error)
}

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// StreamReader allows consuming streaming responses token by token.
//
// StreamReader 允许逐 token 消费流式响应。
type StreamReader interface {
	// Recv reads the next chunk. Returns io.EOF when the stream is complete.
	// Recv 读取下一个 chunk，流结束时返回 io.EOF。
	Recv() (*types.Message, error)
	// Close releases resources associated with the stream.
	// Close 释放流相关资源。
	Close() error
	// Done returns a channel that is closed when the stream has been fully consumed or closed.
	// This allows callers to select on stream completion without blocking on Recv.
	//
	// Done 返回在流消费完毕或关闭时触发的 channel，允许调用者无阻塞地等待流完成。
	Done() <-chan struct{}
}

// DefaultCountTokens provides a rough token estimation based on message content length.
// Providers that lack a dedicated token-counting API should delegate to this function.
//
// DefaultCountTokens 提供基于消息内容长度的粗略 token 估算。
func DefaultCountTokens(messages []*types.Message) int {
	return types.EstimateTokens(messages)
}

// GenOption configures a chat model generation call.
// GenOption 是模型调用参数的配置选项函数。
type GenOption func(*GenConfig)

// ❄️ FROZEN — Stable type. Fields and semantics must not change.
//
// GenConfig holds all configurable parameters for a model call.
// GenConfig 保存模型调用的所有可配置参数。
type GenConfig struct {
	Tools          []*ToolInfo
	Temperature    float32
	MaxTokens      int
	TopP           float32
	Stop           []string
	Meta           map[string]any
	ResponseFormat *ResponseFormat
}

// Apply applies all given options to the config.
// Apply 将所有选项应用到配置中。
func (c *GenConfig) Apply(opts []GenOption) {
	for _, opt := range opts {
		opt(c)
	}
}

// ── ResponseFormat ─────────────────────────────────────────────────────────

// ❄️ FROZEN — Stable type. Fields and semantics must not change.
//
// ResponseFormat controls the output format of the model response.
// Maps to the "response_format" parameter in OpenAI-compatible APIs.
//
// ResponseFormat 控制模型输出的格式，对应 OpenAI 兼容 API 的 response_format 参数。
type ResponseFormat struct {
	// Type is one of "text", "json_object", or "json_schema".
	Type string `json:"type"`
	// Schema is the JSON Schema for "json_schema" mode (optional).
	// Use json.RawMessage with a valid JSON Schema document.
	Schema json.RawMessage `json:"json_schema,omitempty"`
}

// ResponseFormatJSON returns a format that asks the model to output valid JSON.
// The model is not constrained to a specific schema.
//
// ResponseFormatJSON 要求模型输出合法 JSON，不约束具体结构。
func ResponseFormatJSON() ResponseFormat {
	return ResponseFormat{Type: "json_object"}
}

// ResponseFormatJSONSchema returns a format that asks the model to output
// JSON conforming to the given JSON Schema.
//
// ResponseFormatJSONSchema 要求模型输出符合给定 JSON Schema 的 JSON。
func ResponseFormatJSONSchema(schema json.RawMessage) ResponseFormat {
	return ResponseFormat{Type: "json_schema", Schema: schema}
}

// WithResponseFormat sets the response format for a model call.
// Only supported by providers that implement the "response_format" parameter
// (OpenAI-compatible, Gemini, etc.).
//
// WithResponseFormat 设置模型调用的响应格式。
func WithResponseFormat(fmt ResponseFormat) GenOption {
	return func(c *GenConfig) {
		c.ResponseFormat = &fmt
	}
}

// WithTools sets the tools available for the model call.
// WithTools 设置模型调用可用的工具列表。
func WithTools(tools []*ToolInfo) GenOption {
	return func(c *GenConfig) {
		c.Tools = tools
	}
}

// WithTemperature sets the temperature for the model call.
// WithTemperature 设置模型调用的 temperature 参数。
func WithTemperature(temp float32) GenOption {
	return func(c *GenConfig) {
		c.Temperature = temp
	}
}

// WithMaxTokens sets the maximum tokens for the model call.
// WithMaxTokens 设置模型调用的最大 token 数。
func WithMaxTokens(max int) GenOption {
	return func(c *GenConfig) {
		c.MaxTokens = max
	}
}

// WithTopP sets the top-p sampling parameter.
// WithTopP 设置 top-p 采样参数。
func WithTopP(topP float32) GenOption {
	return func(c *GenConfig) {
		c.TopP = topP
	}
}

// WithStop sets the stop sequences.
// WithStop 设置停止序列。
func WithStop(stop []string) GenOption {
	return func(c *GenConfig) {
		c.Stop = stop
	}
}

// WithoutTools disables tool injection for a single CallModel / CallModelStream call.
// Use this for synthesis/refinement phases where tools should not be available.
//
// WithoutTools 在单次 CallModel/CallModelStream 调用中禁用工具注入。
// 用于合成/优化阶段，此时工具不应可用。
func WithoutTools() GenOption {
	return func(c *GenConfig) {
		c.Tools = []*ToolInfo{}
	}
}

// ParseAs unmarshals a JSON response from a structured output into the target.
// target must be a non-nil pointer to a Go struct or map.
// Returns an error if the content is not valid JSON or does not match the target type.
// LLM output that may carry syntax defects (code fences, prose, full-width
// punctuation, ...) should be parsed with the tolerant jsonx package at the
// strategy layer instead — ParseAs stays strict by contract.
//
// Usage:
//
//	type Person struct { Name string `json:"name"`; Age int `json:"age"` }
//	result, _ := agent.Run(ctx, "Get a person")
//	var p Person
//	if err := ParseAs(result.Content, &p); err != nil { ... }
//
// ParseAs 将结构化输出的 JSON 响应解析到目标对象中。
func ParseAs[T any](content string, target *T) error {
	if target == nil {
		return fmt.Errorf("parse: target is nil")
	}
	if content == "" {
		return fmt.Errorf("parse: empty content")
	}
	if err := json.Unmarshal([]byte(content), target); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	return nil
}

// ValidateSchema checks whether content is valid JSON and, when a json_schema
// schema is given, performs a basic top-level type check only. It is NOT a
// full JSON Schema validator: nested properties, required fields, and other
// constraints are not enforced. Callers that need complete schema validation
// must implement it (or use a schema library) at the consumption layer.
//
// ValidateSchema 检查内容是否为合法 JSON；当提供 json_schema 时仅做顶层 type
// 匹配的基础校验。它**不是**完整的 JSON Schema 校验器：嵌套属性、必填字段
// 等约束不会被验证。需要完整 schema 校验的调用方应在消费层自行实现。
func ValidateSchema(content string, schema *ResponseFormat) error {
	if content == "" {
		return fmt.Errorf("validate: empty content")
	}
	// Check JSON validity
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return fmt.Errorf("validate: invalid JSON: %w", err)
	}
	// When schema is provided with json_schema type, validate against it
	if schema != nil && schema.Type == "json_schema" && len(schema.Schema) > 0 {
		var schemaDoc map[string]any
		if err := json.Unmarshal(schema.Schema, &schemaDoc); err != nil {
			return fmt.Errorf("validate: invalid schema: %w", err)
		}
		// Basic validation: check type constraint if present
		if schemaType, ok := schemaDoc["type"].(string); ok {
			var actualType string
			switch v.(type) {
			case map[string]any:
				actualType = "object"
			case []any:
				actualType = "array"
			case string:
				actualType = "string"
			case float64:
				actualType = "number"
			case bool:
				actualType = "boolean"
			default:
				actualType = "unknown"
			}
			if schemaType != actualType {
				return fmt.Errorf("validate: expected type %q, got %q", schemaType, actualType)
			}
		}
	}
	return nil
}

// ModelHandler is a function that performs a model call.
// ModelHandler 是执行模型调用的函数类型。
type ModelHandler func(ctx context.Context, messages []*types.Message) (*types.Message, *types.TokenUsage, error)

// ModelMiddleware wraps a ModelHandler with cross-cutting behavior.
// ModelMiddleware 是包装 ModelHandler 的中间件函数类型。
type ModelMiddleware func(next ModelHandler) ModelHandler

// StreamHandler is a function that performs a streaming model call.
// StreamHandler 是执行流式模型调用的函数类型。
type StreamHandler func(ctx context.Context, messages []*types.Message) (StreamReader, error)

// StreamMiddleware wraps a StreamHandler with cross-cutting behavior.
// StreamMiddleware 是包装 StreamHandler 的中间件函数类型。
type StreamMiddleware func(next StreamHandler) StreamHandler
