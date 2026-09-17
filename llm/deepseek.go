package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// DeepSeekConfig configures a DeepSeek model.
//
// DeepSeekConfig 配置 DeepSeek 模型参数。
type DeepSeekConfig struct {
	// APIKey is the DeepSeek API key.
	APIKey string
	// BaseURL defaults to https://api.deepseek.com.
	BaseURL string
	// Model is the model name (e.g. "deepseek-v4-flash", "deepseek-v4-pro").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
}

// ReasoningEffort controls how much reasoning the model uses.
//
// ReasoningEffort 控制模型的推理深度。
type ReasoningEffort string

const (
	// ReasoningEffortLow uses minimal reasoning (fastest).
	//
	// ReasoningEffortLow 使用最少的推理（最快）。
	ReasoningEffortLow ReasoningEffort = "low"
	// ReasoningEffortMedium uses moderate reasoning.
	//
	// ReasoningEffortMedium 使用中等程度的推理。
	ReasoningEffortMedium ReasoningEffort = "medium"
	// ReasoningEffortHigh uses deep reasoning.
	//
	// ReasoningEffortHigh 使用深度推理。
	ReasoningEffortHigh ReasoningEffort = "high"
	// ReasoningEffortMax uses maximum reasoning (slowest, most thorough).
	//
	// ReasoningEffortMax 使用最大程度的推理（最慢、最彻底）。
	ReasoningEffortMax ReasoningEffort = "max"
)

// ThinkingMode controls whether thinking mode is enabled.
//
// ThinkingMode 控制思考模式是否开启。
type ThinkingMode string

const (
	// ThinkingModeEnabled enables thinking mode (model shows its reasoning).
	//
	// ThinkingModeEnabled 开启思考模式（模型展示其推理过程）。
	ThinkingModeEnabled ThinkingMode = "enabled"
	// ThinkingModeDisabled disables thinking mode.
	//
	// ThinkingModeDisabled 关闭思考模式。
	ThinkingModeDisabled ThinkingMode = "disabled"
)

// deepseekRequest extends the OpenAI chat request with DeepSeek-specific fields.
type deepseekRequest struct {
	Model           string               `json:"model"`
	Messages        []deepseekMessage    `json:"messages"`
	Stream          bool                 `json:"stream"`
	StreamOptions   *openaiStreamOptions `json:"stream_options,omitempty"`
	Temperature     float32              `json:"temperature,omitempty"`
	MaxTokens       int                  `json:"max_tokens,omitempty"`
	TopP            float32              `json:"top_p,omitempty"`
	Stop            []string             `json:"stop,omitempty"`
	Tools           []openaiTool         `json:"tools,omitempty"`
	ToolChoice      any                  `json:"tool_choice,omitempty"`
	ReasoningEffort ReasoningEffort      `json:"reasoning_effort,omitempty"`
	Thinking        *deepseekThinking    `json:"thinking,omitempty"`
}

type deepseekThinking struct {
	Type string `json:"type"`
}

// deepseekResponse extends the OpenAI response with DeepSeek-specific fields.
type deepseekResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []deepseekChoice `json:"choices"`
	Usage   *openaiUsage     `json:"usage,omitempty"`
	Error   *openaiError     `json:"error,omitempty"`
}

type deepseekChoice struct {
	Index        int             `json:"index"`
	Message      deepseekMessage `json:"message,omitempty"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

type deepseekMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	Name             string           `json:"name,omitempty"`
}

// deepseekStreamChunk is a single SSE chunk from DeepSeek streaming.
type deepseekStreamChunk struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []deepseekStreamChoice `json:"choices"`
	Usage   *openaiUsage           `json:"usage,omitempty"`
}

type deepseekStreamChoice struct {
	Index        int             `json:"index"`
	Delta        deepseekMessage `json:"delta"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

// DeepSeekModel implements kernel.ChatModel for DeepSeek.
//
// DeepSeekModel 为 DeepSeek 实现 kernel.ChatModel 接口。
type DeepSeekModel struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewDeepSeekModel creates a new DeepSeek ChatModel.
// Default baseURL is https://api.deepseek.com.
//
// NewDeepSeekModel 创建一个新的 DeepSeek ChatModel。默认 baseURL 为
// https://api.deepseek.com。
func NewDeepSeekModel(model, apiKey string, cfg *DeepSeekConfig) *DeepSeekModel {
	if cfg == nil {
		cfg = &DeepSeekConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	return &DeepSeekModel{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  client,
	}
}

// Generate performs a synchronous chat completion via DeepSeek.
//
// Generate 通过 DeepSeek 执行同步的聊天补全。
func (m *DeepSeekModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("deepseek generate: %w", err)
	}

	if resp.Error != nil {
		return nil, nil, fmt.Errorf("deepseek API error: %s (type: %s, code: %s)",
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("deepseek: empty response (no choices)")
	}

	msg := deepseekToMessage(&resp.Choices[0].Message)
	var usage *types.TokenUsage
	if resp.Usage != nil {
		usage = &types.TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}
	return msg, usage, nil
}

// Stream performs a streaming chat completion via DeepSeek.
//
// Stream 通过 DeepSeek 执行流式聊天补全。
func (m *DeepSeekModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("deepseek stream: %w", err)
	}

	return newDeepSeekStreamReader(body), nil
}

// CountTokens returns an estimated token count for the given messages.
//
// CountTokens 返回给定消息的 token 数量估算。
func (m *DeepSeekModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

// messagesToDeepSeek converts gocel messages to DeepSeek request format.
func messagesToDeepSeek(msgs []*types.Message) []deepseekMessage {
	result := make([]deepseekMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		rm := deepseekMessage{
			Role:             string(m.Role),
			ReasoningContent: m.ReasoningContent,
			ToolCallID:       m.ToolCallID,
			Name:             m.ToolName,
		}
		if m.IsMultimodal() {
			rm.Content = contentPartsToOpenAI(m.ContentParts)
		} else {
			rm.Content = m.Content
		}
		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				rm.ToolCalls = append(rm.ToolCalls, openaiToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: openaiToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
					Index: tc.Index,
				})
			}
		}
		result = append(result, rm)
	}
	return result
}

func (m *DeepSeekModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *deepseekRequest {
	req := &deepseekRequest{
		Model:    m.model,
		Messages: messagesToDeepSeek(messages),
		Stream:   stream,
	}
	if stream {
		// C1: request the final usage chunk.
		req.StreamOptions = &openaiStreamOptions{IncludeUsage: true}
	}

	if cfg.Temperature > 0 {
		req.Temperature = cfg.Temperature
	}
	if cfg.MaxTokens > 0 {
		req.MaxTokens = cfg.MaxTokens
	}
	if cfg.TopP > 0 {
		req.TopP = cfg.TopP
	}
	if len(cfg.Stop) > 0 {
		req.Stop = cfg.Stop
	}

	for _, t := range cfg.Tools {
		req.Tools = append(req.Tools, openaiTool{
			Type: "function",
			Function: openaiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	if v, ok := cfg.Meta["reasoning_effort"]; ok {
		if s, ok := v.(ReasoningEffort); ok {
			req.ReasoningEffort = s
		} else if s, ok := v.(string); ok {
			req.ReasoningEffort = ReasoningEffort(s)
		}
	}

	if v, ok := cfg.Meta["thinking"]; ok {
		if s, ok := v.(string); ok {
			req.Thinking = &deepseekThinking{Type: s}
		}
	}

	return req
}

func (m *DeepSeekModel) doRequest(ctx context.Context, req *deepseekRequest) (*deepseekResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.JoinPath(m.baseURL, "/chat/completions")
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)

	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", transportError(m.model, err))
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp deepseekResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, apiErrorf(m.model, httpResp.StatusCode, "deepseek API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("deepseek API error: %w", apiError(m.model, httpResp.StatusCode, body))
	}

	var resp deepseekResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *DeepSeekModel) doRequestRaw(ctx context.Context, req *deepseekRequest) (io.ReadCloser, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.JoinPath(m.baseURL, "/chat/completions")
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)

	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", transportError(m.model, err))
	}

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		var errResp deepseekResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, apiErrorf(m.model, httpResp.StatusCode, "deepseek API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("deepseek API error: %w", apiError(m.model, httpResp.StatusCode, body))
	}

	return httpResp.Body, nil
}

// deepseekStreamReader adapts the shared OpenAI-style SSE state machine
// (C7): chunk decoding is the only provider-specific part.
type deepseekStreamReader struct {
	*openaiStyleStreamReader
}

func newDeepSeekStreamReader(body io.ReadCloser) *deepseekStreamReader {
	return &deepseekStreamReader{openaiStyleStreamReader: newOpenAIStyleStreamReader(body,
		func(data []byte) (sseDelta, string, *openaiUsage, bool, error) {
			var chunk deepseekStreamChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				return nil, "", nil, false, fmt.Errorf("stream decode: %w", err)
			}
			if len(chunk.Choices) == 0 {
				return nil, "", chunk.Usage, true, nil
			}
			c := chunk.Choices[0]
			finish := ""
			if c.FinishReason != nil {
				finish = *c.FinishReason
			}
			return c.Delta, finish, chunk.Usage, true, nil
		},
		func(err error) error { return fmt.Errorf("deepseek %w", err) },
	)}
}
func deepseekToMessage(msg *deepseekMessage) *types.Message {
	if msg == nil {
		return nil
	}

	content, _ := msg.Content.(string) // checked earlier in Generate
	gocelMsg := &types.Message{
		Role:             types.Role(msg.Role),
		Content:          content,
		ReasoningContent: msg.ReasoningContent,
	}

	for _, tc := range msg.ToolCalls {
		gocelMsg.ToolCalls = append(gocelMsg.ToolCalls, types.ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: types.ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}

	return gocelMsg
}

// ReasoningEffortMeta returns a GenOption to set the reasoning effort.
//
// ReasoningEffortMeta 返回一个用于设置推理深度的 GenOption。
func ReasoningEffortMeta(effort ReasoningEffort) kernel.GenOption {
	return func(c *kernel.GenConfig) {
		if c.Meta == nil {
			c.Meta = make(map[string]any)
		}
		c.Meta["reasoning_effort"] = effort
	}
}

// ThinkingMeta returns a GenOption to enable/disable thinking mode.
//
// ThinkingMeta 返回一个用于开启/关闭思考模式的 GenOption。
func ThinkingMeta(mode ThinkingMode) kernel.GenOption {
	return func(c *kernel.GenConfig) {
		if c.Meta == nil {
			c.Meta = make(map[string]any)
		}
		c.Meta["thinking"] = string(mode)
	}
}
