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

// QwenConfig configures an Alibaba Cloud Qwen model.
//
// QwenConfig 配置阿里云通义千问模型。
type QwenConfig struct {
	// APIKey is the DashScope API key.
	APIKey string
	// BaseURL defaults to https://dashscope.aliyuncs.com/compatible-mode/v1.
	BaseURL string
	// Model is the model name (e.g. "qwen-plus", "qwen-max", "qwen-turbo", "qwen-vl-plus").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
}

// qwenMessage is a message in Qwen's request/response format.
type qwenMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	Name             string           `json:"name,omitempty"`
}

// qwenChatResponse is the response from Qwen's chat completions endpoint.
type qwenChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []qwenChoice `json:"choices"`
	Usage   *openaiUsage `json:"usage,omitempty"`
	Error   *openaiError `json:"error,omitempty"`
}

type qwenChoice struct {
	Index        int         `json:"index"`
	Message      qwenMessage `json:"message,omitempty"`
	FinishReason *string     `json:"finish_reason,omitempty"`
}

// qwenRequest extends the OpenAI request with Qwen-specific fields.
type qwenRequest struct {
	Model                  string        `json:"model"`
	Messages               []qwenMessage `json:"messages"`
	Stream                 bool          `json:"stream"`
	Temperature            float32       `json:"temperature,omitempty"`
	MaxTokens              int           `json:"max_tokens,omitempty"`
	TopP                   float32       `json:"top_p,omitempty"`
	Stop                   []string      `json:"stop,omitempty"`
	Tools                  []openaiTool  `json:"tools,omitempty"`
	ToolChoice             any           `json:"tool_choice,omitempty"`
	EnableSearch           bool          `json:"enable_search,omitempty"`
	VLHighResolutionImages bool          `json:"vl_high_resolution_images,omitempty"`
	Modalities             []string      `json:"modalities,omitempty"`
	Audio                  *qwenAudio    `json:"audio,omitempty"`
}

type qwenAudio struct {
	Voice  string `json:"voice"`
	Format string `json:"format"`
}

// QwenModel implements kernel.ChatModel for Alibaba Cloud Qwen.
//
// QwenModel 为阿里云通义千问实现 kernel.ChatModel。
type QwenModel struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewQwenModel creates a new Alibaba Cloud Qwen ChatModel.
//
// NewQwenModel 创建一个新的阿里云通义千问 ChatModel。
func NewQwenModel(model, apiKey string, cfg *QwenConfig) *QwenModel {
	if cfg == nil {
		cfg = &QwenConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
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

	return &QwenModel{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  client,
	}
}

// Generate performs a synchronous chat completion.
//
// Generate 执行一次同步聊天补全。
func (m *QwenModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("qwen generate: %w", err)
	}

	if resp.Error != nil {
		return nil, nil, fmt.Errorf("qwen API error: %s (type: %s, code: %s)",
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("qwen: empty response (no choices)")
	}

	msg := qwenRespToMessage(&resp.Choices[0].Message)
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

// messagesToQwen converts gocel messages to Qwen request format.
func messagesToQwen(msgs []*types.Message) []qwenMessage {
	result := make([]qwenMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		rm := qwenMessage{
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

// qwenRespToMessage converts a Qwen response message to gocel format.
func qwenRespToMessage(msg *qwenMessage) *types.Message {
	if msg == nil {
		return nil
	}
	content, _ := msg.Content.(string)
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

// Stream performs a streaming chat completion.
//
// Stream 执行一次流式聊天补全。
func (m *QwenModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("qwen stream: %w", err)
	}

	return newOpenAIStreamReader(body), nil
}

// CountTokens estimates the token count of the given messages.
//
// CountTokens 估算给定消息的 token 数量。
func (m *QwenModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

func (m *QwenModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *qwenRequest {
	req := &qwenRequest{
		Model:    m.model,
		Messages: messagesToQwen(messages),
		Stream:   stream,
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

	// Qwen-specific features via Meta
	if cfg.Meta != nil {
		if v, ok := cfg.Meta["enable_search"]; ok {
			req.EnableSearch, _ = v.(bool)
		}
		if v, ok := cfg.Meta["vl_high_resolution_images"]; ok {
			req.VLHighResolutionImages, _ = v.(bool)
		}
		if v, ok := cfg.Meta["modalities"]; ok {
			if mods, ok := v.([]string); ok {
				req.Modalities = mods
			}
		}
		if v, ok := cfg.Meta["audio"]; ok {
			if audio, ok := v.(*qwenAudio); ok {
				req.Audio = audio
			}
		}
	}

	return req
}

func (m *QwenModel) doRequest(ctx context.Context, req *qwenRequest) (*qwenChatResponse, error) {
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
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp qwenChatResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, fmt.Errorf("qwen API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("qwen API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var resp qwenChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *QwenModel) doRequestRaw(ctx context.Context, req *qwenRequest) (io.ReadCloser, error) {
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
		return nil, fmt.Errorf("http request: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		var errResp qwenChatResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, fmt.Errorf("qwen API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("qwen API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	return httpResp.Body, nil
}
