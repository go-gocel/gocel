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

// HunyuanConfig configures a Tencent Hunyuan model via the OpenAI-compatible API.
//
// HunyuanConfig 通过 OpenAI 兼容 API 配置腾讯混元模型。
type HunyuanConfig struct {
	// APIKey is the Hunyuan API key from Tencent Cloud console.
	APIKey string
	// BaseURL defaults to https://api.hunyuan.cloud.tencent.com/v1.
	BaseURL string
	// Model is the model name (e.g. "hunyuan-turbos-latest", "hunyuan-pro", "hunyuan-vision").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
	// EnableEnhancement enables search enhancement (鍔熻兘澧炲己).
	EnableEnhancement bool
	// ForceSearchEnhancement forces AI search even when results may be empty.
	ForceSearchEnhancement bool
	// EnableMultimedia enables multimedia output (鐧藉悕鍗曞姛鑳?.
	EnableMultimedia bool
	// EnableRecommendedQuestions enables recommended questions in response.
	EnableRecommendedQuestions bool
	// SearchInfo returns search info when true and search is triggered.
	SearchInfo bool
	// Citation adds citation markers in search-enhanced responses.
	Citation bool
	// EnableSpeedSearch enables fast search for lower first-token latency.
	EnableSpeedSearch bool
}

// hunyuanMessage is a message in Hunyuan's request/response format.
type hunyuanMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	Name             string           `json:"name,omitempty"`
}

// hunyuanChatResponse is the response from Hunyuan's chat completions endpoint.
type hunyuanChatResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []hunyuanChoice `json:"choices"`
	Usage   *openaiUsage    `json:"usage,omitempty"`
	Error   *openaiError    `json:"error,omitempty"`
}

type hunyuanChoice struct {
	Index        int            `json:"index"`
	Message      hunyuanMessage `json:"message,omitempty"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

// hunyuanRequest extends the OpenAI request with Hunyuan-specific fields.
type hunyuanRequest struct {
	Model         string           `json:"model"`
	Messages      []hunyuanMessage `json:"messages"`
	Stream        bool             `json:"stream"`
	Temperature   float32          `json:"temperature,omitempty"`
	MaxTokens     int              `json:"max_tokens,omitempty"`
	TopP          float32          `json:"top_p,omitempty"`
	Stop          []string         `json:"stop,omitempty"`
	Seed          int              `json:"seed,omitempty"`
	Tools         []openaiTool     `json:"tools,omitempty"`
	ToolChoice    any              `json:"tool_choice,omitempty"`
	StreamOptions *streamOptions   `json:"stream_options,omitempty"`

	// Hunyuan custom parameters
	EnableEnhancement          bool `json:"enable_enhancement,omitempty"`
	ForceSearchEnhancement     bool `json:"force_search_enhancement,omitempty"`
	EnableMultimedia           bool `json:"enable_multimedia,omitempty"`
	EnableRecommendedQuestions bool `json:"enable_recommended_questions,omitempty"`
	SearchInfo                 bool `json:"search_info,omitempty"`
	Citation                   bool `json:"citation,omitempty"`
	EnableSpeedSearch          bool `json:"enable_speed_search,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// HunyuanModel implements kernel.ChatModel for Tencent Hunyuan.
//
// HunyuanModel 为腾讯混元实现 kernel.ChatModel 接口。
type HunyuanModel struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
	cfg     HunyuanConfig
}

// NewHunyuanModel creates a new Tencent Hunyuan ChatModel using the OpenAI-compatible API.
//
// NewHunyuanModel 使用 OpenAI 兼容 API 创建一个新的腾讯混元 ChatModel。
func NewHunyuanModel(model, apiKey string, cfg *HunyuanConfig) *HunyuanModel {
	if cfg == nil {
		cfg = &HunyuanConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.hunyuan.cloud.tencent.com/v1"
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

	return &HunyuanModel{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  client,
		cfg:     *cfg,
	}
}

// messagesToHunyuan converts gocel messages to Hunyuan request format.
func messagesToHunyuan(msgs []*types.Message) []hunyuanMessage {
	result := make([]hunyuanMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		rm := hunyuanMessage{
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

// hunyuanRespToMessage converts a Hunyuan response message to gocel format.
func hunyuanRespToMessage(msg *hunyuanMessage) *types.Message {
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

// Generate performs a synchronous chat completion via Hunyuan.
//
// Generate 通过混元执行同步的聊天补全。
func (m *HunyuanModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("hunyuan generate: %w", err)
	}

	if resp.Error != nil {
		return nil, nil, fmt.Errorf("hunyuan API error: %s (type: %s, code: %s)",
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("hunyuan: empty response (no choices)")
	}

	msg := hunyuanRespToMessage(&resp.Choices[0].Message)
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

// Stream performs a streaming chat completion via Hunyuan.
//
// Stream 通过混元执行流式聊天补全。
func (m *HunyuanModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("hunyuan stream: %w", err)
	}

	return newOpenAIStreamReader(body), nil
}

// CountTokens returns an estimated token count for the given messages.
//
// CountTokens 返回给定消息的 token 数量估算。
func (m *HunyuanModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

func (m *HunyuanModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *hunyuanRequest {
	req := &hunyuanRequest{
		Model:                      m.model,
		Messages:                   messagesToHunyuan(messages),
		Stream:                     stream,
		EnableEnhancement:          m.cfg.EnableEnhancement,
		ForceSearchEnhancement:     m.cfg.ForceSearchEnhancement,
		EnableMultimedia:           m.cfg.EnableMultimedia,
		EnableRecommendedQuestions: m.cfg.EnableRecommendedQuestions,
		SearchInfo:                 m.cfg.SearchInfo,
		Citation:                   m.cfg.Citation,
		EnableSpeedSearch:          m.cfg.EnableSpeedSearch,
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

	// Seed
	if cfg.Meta != nil {
		if v, ok := cfg.Meta["seed"]; ok {
			if seed, ok := v.(int); ok {
				req.Seed = seed
			}
		}
		// Support override of Hunyuan-specific params via Meta
		if v, ok := cfg.Meta["enable_enhancement"]; ok {
			req.EnableEnhancement, _ = v.(bool)
		}
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

	return req
}

func (m *HunyuanModel) doRequest(ctx context.Context, req *hunyuanRequest) (*hunyuanChatResponse, error) {
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
		var errResp hunyuanChatResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, fmt.Errorf("hunyuan API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("hunyuan API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var resp hunyuanChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *HunyuanModel) doRequestRaw(ctx context.Context, req *hunyuanRequest) (io.ReadCloser, error) {
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
		var errResp hunyuanChatResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return nil, fmt.Errorf("hunyuan API error (status %d): %s (type: %s, code: %s)",
				httpResp.StatusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
		return nil, fmt.Errorf("hunyuan API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	return httpResp.Body, nil
}
