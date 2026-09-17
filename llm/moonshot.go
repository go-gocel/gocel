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

// MoonshotConfig configures a Moonshot (Kimi) model.
//
// MoonshotConfig 配置 Moonshot（Kimi）模型。
type MoonshotConfig struct {
	// APIKey is the Moonshot API key.
	APIKey string
	// BaseURL defaults to https://api.moonshot.cn/v1.
	BaseURL string
	// Model is the model name (e.g. "kimi-k2.6", "kimi-k2.5", "moonshot-v1-128k").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
}

// moonshotThinking controls the thinking mode for K2 models.
type moonshotThinking struct {
	Type string `json:"type"`           // "enabled" or "disabled"
	Keep string `json:"keep,omitempty"` // "all" or null
}

// moonshotResponseFormat controls the output format.
type moonshotResponseFormat struct {
	Type       string              `json:"type"` // "text", "json_object", "json_schema"
	JSONSchema *moonshotJSONSchema `json:"json_schema,omitempty"`
}

type moonshotJSONSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

// moonshotRequest is the request body for Moonshot's /v1/chat/completions.
type moonshotRequest struct {
	Model               string                  `json:"model"`
	Messages            []moonshotReqMessage    `json:"messages"`
	Stream              bool                    `json:"stream"`
	StreamOptions       *openaiStreamOptions    `json:"stream_options,omitempty"`
	MaxCompletionTokens int                     `json:"max_completion_tokens,omitempty"`
	Temperature         float32                 `json:"temperature,omitempty"`
	TopP                float32                 `json:"top_p,omitempty"`
	Stop                []string                `json:"stop,omitempty"`
	Tools               []openaiTool            `json:"tools,omitempty"`
	ToolChoice          any                     `json:"tool_choice,omitempty"`
	N                   int                     `json:"n,omitempty"`
	PresencePenalty     float32                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty    float32                 `json:"frequency_penalty,omitempty"`
	ResponseFormat      *moonshotResponseFormat `json:"response_format,omitempty"`
	Thinking            *moonshotThinking       `json:"thinking,omitempty"`
	PromptCacheKey      string                  `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier    string                  `json:"safety_identifier,omitempty"`
}

// moonshotReqMessage is a message in the Moonshot request format.
type moonshotReqMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"` // string or []moonshotContentPart
	Name             string           `json:"name,omitempty"`
	Partial          bool             `json:"partial,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
}

// moonshotContentPart is a multimodal content part.
type moonshotContentPart struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	ImageURL *moonshotImageURL `json:"image_url,omitempty"`
	VideoURL *moonshotVideoURL `json:"video_url,omitempty"`
}

type moonshotImageURL struct {
	URL string `json:"url"`
}

type moonshotVideoURL struct {
	URL string `json:"url"`
}

// moonshotResponse is the response from Moonshot.
type moonshotResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []moonshotChoice `json:"choices"`
	Usage   *openaiUsage     `json:"usage,omitempty"`
	Error   *moonshotError   `json:"error,omitempty"`
}

type moonshotChoice struct {
	Index        int             `json:"index"`
	Message      moonshotRespMsg `json:"message,omitempty"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

type moonshotRespMsg struct {
	Role             string           `json:"role"`
	Content          any              `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

type moonshotError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// moonshotStreamChunk is a single SSE chunk.
type moonshotStreamChunk struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []moonshotStreamChoice `json:"choices"`
	Usage   *openaiUsage           `json:"usage,omitempty"`
}

type moonshotStreamChoice struct {
	Index        int             `json:"index"`
	Delta        moonshotRespMsg `json:"delta"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

// MoonshotModel implements kernel.ChatModel for Moonshot (Kimi).
//
// MoonshotModel 为 Moonshot（Kimi）实现 kernel.ChatModel 接口。
type MoonshotModel struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewMoonshotModel creates a new Moonshot (Kimi) ChatModel.
//
// NewMoonshotModel 创建一个新的 Moonshot（Kimi）ChatModel。
func NewMoonshotModel(model, apiKey string, cfg *MoonshotConfig) *MoonshotModel {
	if cfg == nil {
		cfg = &MoonshotConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.moonshot.cn/v1"
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

	return &MoonshotModel{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  client,
	}
}

// Generate performs a synchronous chat completion.
//
// Generate 执行同步的聊天补全。
func (m *MoonshotModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("moonshot generate: %w", err)
	}

	if resp.Error != nil {
		return nil, nil, fmt.Errorf("moonshot API error: %s (type: %s, code: %s)",
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("moonshot: empty response (no choices)")
	}

	msg := moonshotRespToMessage(&resp.Choices[0].Message)
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
func (m *MoonshotModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("moonshot stream: %w", err)
	}

	return newMoonshotStreamReader(body), nil
}

// CountTokens returns an estimated token count for the given messages.
//
// CountTokens 返回给定消息的 token 数量估算。
func (m *MoonshotModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

func (m *MoonshotModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *moonshotRequest {
	req := &moonshotRequest{
		Model:  m.model,
		Stream: stream,
	}
	if stream {
		// C1: request the final usage chunk.
		req.StreamOptions = &openaiStreamOptions{IncludeUsage: true}
	}

	req.Messages = messagesToMoonshot(messages)

	if cfg.Temperature > 0 {
		req.Temperature = cfg.Temperature
	}
	if cfg.MaxTokens > 0 {
		req.MaxCompletionTokens = cfg.MaxTokens
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

	// Meta passthrough for Moonshot-specific fields
	if cfg.Meta != nil {
		if v, ok := cfg.Meta["thinking"]; ok {
			if m, ok := v.(map[string]string); ok {
				t := &moonshotThinking{Type: m["type"]}
				if keep, ok := m["keep"]; ok {
					t.Keep = keep
				}
				req.Thinking = t
			}
		}
		if v, ok := cfg.Meta["prompt_cache_key"]; ok {
			req.PromptCacheKey, _ = v.(string)
		}
		if v, ok := cfg.Meta["safety_identifier"]; ok {
			req.SafetyIdentifier, _ = v.(string)
		}
		if v, ok := cfg.Meta["n"]; ok {
			if n, ok := v.(int); ok {
				req.N = n
			}
		}
	}
	// Response format
	if cfg.Meta != nil {
		if v, ok := cfg.Meta["response_format"]; ok {
			if rf, ok := v.(*moonshotResponseFormat); ok {
				req.ResponseFormat = rf
			}
		}
	}

	return req
}

func (m *MoonshotModel) doRequest(ctx context.Context, req *moonshotRequest) (*moonshotResponse, error) {
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
		var errResp moonshotResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, apiErrorf(m.model, httpResp.StatusCode, "moonshot API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("moonshot API error: %w", apiError(m.model, httpResp.StatusCode, body))
	}

	var resp moonshotResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *MoonshotModel) doRequestRaw(ctx context.Context, req *moonshotRequest) (io.ReadCloser, error) {
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
		var errResp moonshotResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, apiErrorf(m.model, httpResp.StatusCode, "moonshot API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("moonshot API error: %w", apiError(m.model, httpResp.StatusCode, body))
	}

	return httpResp.Body, nil
}

// moonshotStreamReader adapts the shared OpenAI-style SSE state machine
// (C7): chunk decoding is the only provider-specific part. Malformed
// chunks are tolerated (skipped) — the provider occasionally emits
// non-chunk lines.
type moonshotStreamReader struct {
	*openaiStyleStreamReader
}

func newMoonshotStreamReader(body io.ReadCloser) *moonshotStreamReader {
	return &moonshotStreamReader{openaiStyleStreamReader: newOpenAIStyleStreamReader(body,
		func(data []byte) (sseDelta, string, *openaiUsage, bool, error) {
			var chunk moonshotStreamChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				return nil, "", nil, false, nil // tolerate malformed chunks
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
		func(err error) error { return fmt.Errorf("moonshot %w", err) },
	)}
}
func messagesToMoonshot(msgs []*types.Message) []moonshotReqMessage {
	result := make([]moonshotReqMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		rm := moonshotReqMessage{
			Role:             string(m.Role),
			Name:             m.ToolName,
			ToolCallID:       m.ToolCallID,
			ReasoningContent: m.ReasoningContent,
		}

		if m.IsMultimodal() {
			rm.Content = contentPartsToMoonshot(m.ContentParts)
		} else {
			rm.Content = m.Content
		}

		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				idx := tc.Index
				rm.ToolCalls = append(rm.ToolCalls, openaiToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: openaiToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
					Index: idx,
				})
			}
		}

		result = append(result, rm)
	}
	return result
}

func contentPartsToMoonshot(parts []types.ContentPart) []moonshotContentPart {
	result := make([]moonshotContentPart, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case types.ContentTypeText:
			result = append(result, moonshotContentPart{Type: "text", Text: p.Text})
		case types.ContentTypeImageURL:
			result = append(result, moonshotContentPart{
				Type:     "image_url",
				ImageURL: &moonshotImageURL{URL: p.ImageURL},
			})
		case types.ContentTypeImageData:
			mime := "image/png"
			if p.ImageData != nil && p.ImageData.MIMEType != "" {
				mime = p.ImageData.MIMEType
			}
			result = append(result, moonshotContentPart{
				Type: "image_url",
				ImageURL: &moonshotImageURL{
					URL: "data:" + mime + ";base64," + p.ImageData.Data,
				},
			})
		case types.ContentTypeVideoData:
			mime := "video/mp4"
			if p.VideoData != nil && p.VideoData.MIMEType != "" {
				mime = p.VideoData.MIMEType
			}
			result = append(result, moonshotContentPart{
				Type: "video_url",
				VideoURL: &moonshotVideoURL{
					URL: "data:" + mime + ";base64," + p.VideoData.Data,
				},
			})
		default:
			result = append(result, moonshotContentPart{Type: "text", Text: p.Text})
		}
	}
	return result
}

func moonshotRespToMessage(msg *moonshotRespMsg) *types.Message {
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
