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

// OpenAIConfig configures an OpenAI-compatible chat model.
//
// OpenAIConfig 配置 OpenAI 兼容的聊天模型。
type OpenAIConfig struct {
	// APIKey is the OpenAI API key (or compatible provider's key).
	APIKey string
	// BaseURL is the API base URL (default: https://api.openai.com/v1).
	// For compatible providers, set to their endpoint (e.g. https://api.deepseek.com/v1).
	BaseURL string
	// Model is the model name (e.g. "gpt-4", "gpt-4o", "deepseek-chat").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
	// OrganizationID sets the OpenAI organization header.
	OrganizationID string
}

// openaiChatRequest is the request body for OpenAI's /chat/completions endpoint.
type openaiChatRequest struct {
	Model          string                 `json:"model"`
	Messages       []openaiMessage        `json:"messages"`
	Stream         bool                   `json:"stream"`
	StreamOptions  *openaiStreamOptions   `json:"stream_options,omitempty"`
	Temperature    float32                `json:"temperature,omitempty"`
	MaxTokens      int                    `json:"max_tokens,omitempty"`
	TopP           float32                `json:"top_p,omitempty"`
	Stop           []string               `json:"stop,omitempty"`
	Tools          []openaiTool           `json:"tools,omitempty"`
	ToolChoice     any                    `json:"tool_choice,omitempty"`
	ResponseFormat *kernel.ResponseFormat `json:"response_format,omitempty"`
	Meta           map[string]any         `json:"-"`
}

// openaiStreamOptions asks the provider for a final usage-only chunk in
// stream mode (C1) — without include_usage many providers never report
// streaming usage at all.
type openaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// openaiContentPart is a content part in OpenAI's format (for multimodal messages).
type openaiContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openaiImageURL `json:"image_url,omitempty"`
}

type openaiImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// openaiMessage is a message in OpenAI's format.
type openaiMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"` // string or []openaiContentPart
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	Name             string           `json:"name,omitempty"`
}

// openaiToolCall represents a tool call in OpenAI's format.
type openaiToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openaiToolCallFunction `json:"function"`
	Index    *int                   `json:"index,omitempty"`
}

type openaiToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// openaiTool defines a tool for OpenAI.
type openaiTool struct {
	Type     string         `json:"type"`
	Function openaiFunction `json:"function"`
}

type openaiFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// openaiChatResponse is the response from OpenAI's /chat/completions.
type openaiChatResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
	Error   *openaiError   `json:"error,omitempty"`
}

type openaiChoice struct {
	Index        int           `json:"index"`
	Message      openaiMessage `json:"message,omitempty"`
	Delta        openaiMessage `json:"delta,omitempty"`
	FinishReason *string       `json:"finish_reason,omitempty"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// UnmarshalJSON handles both {"error": {"message": "...", "type": "..."}} and {"error": "plain string"}.
//
// UnmarshalJSON 同时支持 {"error": {"message": "...", "type": "..."}} 与
// {"error": "plain string"} 两种错误格式。
func (e *openaiError) UnmarshalJSON(data []byte) error {
	// Try object first
	var obj struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		e.Message = obj.Message
		e.Type = obj.Type
		e.Code = obj.Code
		return nil
	}
	// Fallback: try plain string
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		e.Message = s
		return nil
	}
	return fmt.Errorf("cannot unmarshal %s into openaiError", string(data))
}

// openaiStreamChunk is a single SSE chunk from OpenAI streaming.
type openaiStreamChunk struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []openaiStreamChoice `json:"choices"`
	Usage   *openaiUsage         `json:"usage,omitempty"`
}

type openaiStreamChoice struct {
	Index        int           `json:"index"`
	Delta        openaiMessage `json:"delta"`
	FinishReason *string       `json:"finish_reason,omitempty"`
}

// OpenAIModel implements kernel.ChatModel for OpenAI-compatible APIs.
//
// OpenAIModel 为 OpenAI 兼容 API 实现 kernel.ChatModel 接口。
type OpenAIModel struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
	orgID   string
}

// NewOpenAICompatibleModel creates a ChatModel for any OpenAI-compatible API.
// This is the universal constructor for the hundreds of providers that support
// the OpenAI chat completions format. Just pass the model name, API key, and base URL.
//
// Usage:
//
//	model := llm.NewOpenAICompatibleModel("gpt-4", "sk-...", "https://api.openai.com/v1")
//	model := llm.NewOpenAICompatibleModel("deepseek-chat", "sk-...", "https://api.deepseek.com")
//	model := llm.NewOpenAICompatibleModel("qwen-plus", "sk-...", "https://dashscope.aliyuncs.com/compatible-mode/v1")
//
// NewOpenAICompatibleModel 为任意 OpenAI 兼容 API 创建 ChatModel。这是面向
// 支持 OpenAI chat completions 格式的众多提供者的通用构造函数，只需传入
// 模型名、API key 与 base URL。
func NewOpenAICompatibleModel(model, apiKey, baseURL string) *OpenAIModel {
	return NewOpenAIModel(model, apiKey, &OpenAIConfig{BaseURL: baseURL})
}

// NewOpenAIModel creates a new OpenAI-compatible ChatModel with a config object.
// Default baseURL is https://api.openai.com/v1.
//
// NewOpenAIModel 使用配置对象创建一个新的 OpenAI 兼容 ChatModel。默认
// baseURL 为 https://api.openai.com/v1。
func NewOpenAIModel(model, apiKey string, cfg *OpenAIConfig) *OpenAIModel {
	if cfg == nil {
		cfg = &OpenAIConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
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

	return &OpenAIModel{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  client,
		orgID:   cfg.OrganizationID,
	}
}

// Generate performs a synchronous chat completion.
//
// Generate 执行同步的聊天补全。
func (m *OpenAIModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("openai generate: %w", err)
	}

	if resp.Error != nil {
		return nil, nil, fmt.Errorf("openai API error: %s (type: %s, code: %s)",
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("openai: empty response (no choices)")
	}

	msg := openaiToMessage(&resp.Choices[0].Message)
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

// Stream performs a streaming chat completion.
//
// Stream 执行流式聊天补全。
func (m *OpenAIModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openai stream: %w", err)
	}

	return newOpenAIStreamReader(body), nil
}

// CountTokens returns an estimated token count for the given messages.
//
// CountTokens 返回给定消息的 token 数量估算。
func (m *OpenAIModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

func (m *OpenAIModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *openaiChatRequest {
	req := &openaiChatRequest{
		Model:    m.model,
		Messages: messagesToOpenAI(messages),
		Stream:   stream,
	}
	if stream {
		// C1: request the final usage chunk; without it many providers
		// never report streaming usage.
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

	// Map tools
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

	// Response format (structured output)
	if cfg.ResponseFormat != nil {
		req.ResponseFormat = cfg.ResponseFormat
	}

	return req
}

func (m *OpenAIModel) doRequest(ctx context.Context, req *openaiChatRequest) (*openaiChatResponse, error) {
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
	if m.orgID != "" {
		httpReq.Header.Set("OpenAI-Organization", m.orgID)
	}

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
		// Try to extract structured error (C7: classified via ModelError).
		var errResp openaiChatResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, apiErrorf(m.model, httpResp.StatusCode,
				"openai API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("openai API error: %w", apiError(m.model, httpResp.StatusCode, body))
	}

	var resp openaiChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *OpenAIModel) doRequestRaw(ctx context.Context, req *openaiChatRequest) (io.ReadCloser, error) {
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
	if m.orgID != "" {
		httpReq.Header.Set("OpenAI-Organization", m.orgID)
	}

	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", transportError(m.model, err))
	}

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		// Try to extract structured error (C7: classified via ModelError).
		var errResp openaiChatResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, apiErrorf(m.model, httpResp.StatusCode,
				"openai API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("openai API error: %w", apiError(m.model, httpResp.StatusCode, body))
	}

	return httpResp.Body, nil
}

// openaiStreamReader adapts the shared OpenAI-style SSE state machine
// (C7): chunk decoding is the only provider-specific part.
type openaiStreamReader struct {
	*openaiStyleStreamReader
}

func newOpenAIStreamReader(body io.ReadCloser) *openaiStreamReader {
	return &openaiStreamReader{openaiStyleStreamReader: newOpenAIStyleStreamReader(body,
		func(data []byte) (sseDelta, string, *openaiUsage, bool, error) {
			var chunk openaiStreamChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				return nil, "", nil, false, fmt.Errorf("stream decode: %w", err)
			}
			if len(chunk.Choices) == 0 {
				// Final usage-only chunk (stream_options.include_usage).
				return nil, "", chunk.Usage, true, nil
			}
			c := chunk.Choices[0]
			finish := ""
			if c.FinishReason != nil {
				finish = *c.FinishReason
			}
			return c.Delta, finish, chunk.Usage, true, nil
		},
		func(err error) error { return fmt.Errorf("openai %w", err) },
	)}
}

// ── Message conversion ───────────────────────────────────────────────

// messagesToOpenAI converts gocel messages to OpenAI format.
func messagesToOpenAI(msgs []*types.Message) []openaiMessage {
	result := make([]openaiMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}

		// OpenAI only accepts media on `user` messages. A tool result that
		// carries multimodal parts therefore splits: the textual part stays
		// on the tool message (preserving the tool_call_id link), and the
		// media parts ride on a trailing user message.
		if m.Role == types.RoleTool && m.IsMultimodal() {
			text, media := splitToolParts(m)
			result = append(result, openaiMessage{
				Role:       string(m.Role),
				Content:    text,
				ToolCallID: m.ToolCallID,
				Name:       m.ToolName,
			})
			if len(media) > 0 {
				result = append(result, openaiMessage{
					Role:    string(types.RoleUser),
					Content: contentPartsToOpenAI(media),
				})
			}
			continue
		}

		om := openaiMessage{
			Role:       string(m.Role),
			ToolCallID: m.ToolCallID,
			Name:       m.ToolName,
		}

		// Handle multimodal content parts
		if m.IsMultimodal() {
			om.Content = contentPartsToOpenAI(m.ContentParts)
		} else {
			om.Content = m.Content
		}

		// Map tool calls from assistant messages
		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				om.ToolCalls = append(om.ToolCalls, openaiToolCall{
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

		result = append(result, om)
	}
	return result
}

// contentPartsToOpenAI converts ContentParts to OpenAI's content array format.
func contentPartsToOpenAI(parts []types.ContentPart) []openaiContentPart {
	result := make([]openaiContentPart, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case types.ContentTypeText:
			result = append(result, openaiContentPart{
				Type: "text",
				Text: p.Text,
			})
		case types.ContentTypeImageURL:
			detail := "auto"
			if p.ImageData != nil && p.ImageData.Detail != "" {
				detail = p.ImageData.Detail
			}
			result = append(result, openaiContentPart{
				Type: "image_url",
				ImageURL: &openaiImageURL{
					URL:    p.ImageURL,
					Detail: detail,
				},
			})
		case types.ContentTypeImageData:
			detail := "auto"
			if p.ImageData != nil && p.ImageData.Detail != "" {
				detail = p.ImageData.Detail
			}
			mime := "image/png"
			if p.ImageData != nil && p.ImageData.MIMEType != "" {
				mime = p.ImageData.MIMEType
			}
			result = append(result, openaiContentPart{
				Type: "image_url",
				ImageURL: &openaiImageURL{
					URL:    "data:" + mime + ";base64," + p.ImageData.Data,
					Detail: detail,
				},
			})
		default:
			// Fail-closed: unsupported content types (audio/video/file) are
			// skipped rather than mis-serialized as empty text. Full support
			// for audio/video/file input is tracked separately.
			//
			// fail-closed：不支持的片段类型（音频/视频/文件）跳过，而非
			// 误序列化为空文本。音频/视频/文件的完整输入支持另行跟进。
		}
	}
	return result
}

// splitToolParts splits a tool result's content parts into a textual string
// and the non-text media parts. Text parts are joined (falling back to the
// message's legacy Content field); media parts are returned as-is so the
// caller can place them on a `user` message (OpenAI restricts media to user).
func splitToolParts(m *types.Message) (string, []types.ContentPart) {
	text := m.Content
	var media []types.ContentPart
	var textParts []string
	for _, p := range m.ContentParts {
		if p.Type == types.ContentTypeText && p.Text != "" {
			textParts = append(textParts, p.Text)
		} else {
			media = append(media, p)
		}
	}
	if len(textParts) > 0 {
		text = strings.Join(textParts, "\n")
	}
	return text, media
}

// openaiToMessage converts an OpenAI response message to gocel format.
func openaiToMessage(msg *openaiMessage) *types.Message {
	if msg == nil {
		return nil
	}

	content, _ := msg.Content.(string)
	gocelMsg := &types.Message{
		Role:    types.Role(msg.Role),
		Content: content,
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
