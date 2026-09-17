package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// BaiduConfig configures a Baidu ERNIE/Qianfan model.
//
// BaiduConfig 配置百度 ERNIE/千帆模型。
type BaiduConfig struct {
	// APIKey is the Baidu API Key (for IAM token auth).
	APIKey string
	// SecretKey is the Baidu Secret Key (for IAM token auth).
	SecretKey string
	// AccessToken is a pre-obtained access token (alternative to APIKey+SecretKey).
	AccessToken string
	// BaseURL defaults to https://aip.baidubce.com.
	BaseURL string
	// Model is the model endpoint name (e.g. "completions", "ernie-4.0-8k", "ernie-3.5-8k").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
}

// baiduTokenResponse is the response from Baidu OAuth token endpoint.
type baiduTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error,omitempty"`
	ErrorDesc   string `json:"error_description,omitempty"`
}

// baiduChatRequest is the request for Baidu ERNIE chat API.
type baiduChatRequest struct {
	Messages        []baiduMessage `json:"messages"`
	Stream          bool           `json:"stream"`
	Temperature     float32        `json:"temperature,omitempty"`
	TopP            float32        `json:"top_p,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
	Stop            []string       `json:"stop,omitempty"`
	System          string         `json:"system,omitempty"`
	PenaltyScore    float32        `json:"penalty_score,omitempty"`
}

type baiduMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// baiduChatResponse is the response from Baidu ERNIE chat API.
type baiduChatResponse struct {
	ID               string             `json:"id"`
	Object           string             `json:"object"`
	Created          int64              `json:"created"`
	Result           string             `json:"result"`
	IsTruncated      bool               `json:"is_truncated"`
	NeedClearHistory bool               `json:"need_clear_history"`
	FinishReason     string             `json:"finish_reason"`
	Usage            *baiduUsage        `json:"usage,omitempty"`
	ErrorCode        int                `json:"error_code,omitempty"`
	ErrorMsg         string             `json:"error_msg,omitempty"`
	FunctionCall     *baiduFunctionCall `json:"function_call,omitempty"`
}

type baiduUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type baiduFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Thoughts  string `json:"thoughts,omitempty"`
}

// baiduStreamChunk is a single streaming chunk from Baidu.
type baiduStreamChunk struct {
	ID               string             `json:"id"`
	Object           string             `json:"object"`
	Created          int64              `json:"created"`
	SentenceID       int                `json:"sentence_id"`
	IsEnd            bool               `json:"is_end"`
	IsTruncated      bool               `json:"is_truncated"`
	Result           string             `json:"result"`
	NeedClearHistory bool               `json:"need_clear_history"`
	FinishReason     string             `json:"finish_reason"`
	Usage            *baiduUsage        `json:"usage,omitempty"`
	FunctionCall     *baiduFunctionCall `json:"function_call,omitempty"`
}

// BaiduModel implements kernel.ChatModel for Baidu ERNIE/Qianfan.
//
// BaiduModel 为百度 ERNIE/千帆实现 kernel.ChatModel 接口。
type BaiduModel struct {
	apiKey      string
	secretKey   string
	accessToken string
	baseURL     string
	model       string
	client      *http.Client

	// directToken reports the AccessToken-only mode (C4): the token is used
	// as-is and never refreshed — the client does not know its expiry, so
	// falling into the AK/SK refresh path would be wrong.
	//
	// directToken 标记「仅 AccessToken」模式（C4）：token 原样使用且永不
	// 刷新——客户端不知道其过期时间，落入 AK/SK 刷新路径是错误的。
	directToken bool

	mu           sync.RWMutex
	tokenExpires time.Time
}

// NewBaiduModel creates a new Baidu ERNIE/Qianfan ChatModel.
// Provide either (apiKey+secretKey) for auto token refresh, or accessToken directly.
// Default baseURL is https://aip.baidubce.com.
//
// NewBaiduModel 创建一个新的百度 ERNIE/千帆 ChatModel。可传
// （apiKey+secretKey）自动刷新 token，或直接传 accessToken。默认
// baseURL 为 https://aip.baidubce.com。
func NewBaiduModel(model, apiKey, secretKey string, cfg *BaiduConfig) *BaiduModel {
	if cfg == nil {
		cfg = &BaiduConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://aip.baidubce.com"
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

	return &BaiduModel{
		apiKey:      apiKey,
		secretKey:   secretKey,
		accessToken: cfg.AccessToken,
		baseURL:     baseURL,
		model:       model,
		client:      client,
		directToken: cfg.AccessToken != "",
	}
}

// Generate performs a synchronous chat completion via Baidu ERNIE.
//
// Generate 通过百度 ERNIE 执行同步的聊天补全。
func (m *BaiduModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("baidu generate: %w", err)
	}

	if resp.ErrorCode != 0 {
		return nil, nil, fmt.Errorf("baidu API error (code %d): %s", resp.ErrorCode, resp.ErrorMsg)
	}

	msg := &types.Message{
		Role:    types.RoleAssistant,
		Content: resp.Result,
	}

	if resp.FunctionCall != nil {
		msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
			ID:   "call_baidu_" + fmt.Sprintf("%d", time.Now().UnixNano()),
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      resp.FunctionCall.Name,
				Arguments: resp.FunctionCall.Arguments,
			},
		})
	}

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

// Stream performs a streaming chat completion via Baidu ERNIE.
//
// Stream 通过百度 ERNIE 执行流式聊天补全。
func (m *BaiduModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("baidu stream: %w", err)
	}

	return newBaiduStreamReader(body), nil
}

// CountTokens returns an estimated token count for the given messages.
//
// CountTokens 返回给定消息的 token 数量估算。
func (m *BaiduModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

func (m *BaiduModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *baiduChatRequest {
	req := &baiduChatRequest{
		Stream: stream,
	}

	// Separate system message
	systemContent := ""
	chatMessages := make([]*types.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == types.RoleSystem {
			if systemContent != "" {
				systemContent += "\n\n" + msg.Content
			} else {
				systemContent = msg.Content
			}
		} else {
			chatMessages = append(chatMessages, msg)
		}
	}

	req.System = systemContent
	req.Messages = messagesToBaidu(chatMessages)

	if cfg.Temperature > 0 {
		req.Temperature = cfg.Temperature
	}
	if cfg.MaxTokens > 0 {
		req.MaxOutputTokens = cfg.MaxTokens
	}
	if cfg.TopP > 0 {
		req.TopP = cfg.TopP
	}
	if len(cfg.Stop) > 0 {
		req.Stop = cfg.Stop
	}

	return req
}

func (m *BaiduModel) getAccessToken(ctx context.Context) (string, error) {
	// Direct-token mode (AccessToken-only): the token is used as-is and
	// never refreshed — there is no AK/SK to refresh with.
	if m.directToken {
		m.mu.RLock()
		token := m.accessToken
		m.mu.RUnlock()
		if token == "" {
			return "", fmt.Errorf("baidu: direct access token is empty")
		}
		return token, nil
	}

	m.mu.RLock()
	if m.accessToken != "" && time.Now().Before(m.tokenExpires) {
		token := m.accessToken
		m.mu.RUnlock()
		return token, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if m.accessToken != "" && time.Now().Before(m.tokenExpires) {
		return m.accessToken, nil
	}

	if m.apiKey == "" || m.secretKey == "" {
		return "", fmt.Errorf("baidu: no API key and secret key configured")
	}

	tokenURL := fmt.Sprintf("%s/oauth/2.0/token?grant_type=client_credentials&client_id=%s&client_secret=%s",
		m.baseURL, url.QueryEscape(m.apiKey), url.QueryEscape(m.secretKey))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}

	var tokenResp baiduTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}

	if tokenResp.Error != "" {
		return "", fmt.Errorf("baidu token error: %s - %s", tokenResp.Error, tokenResp.ErrorDesc)
	}

	m.accessToken = tokenResp.AccessToken
	m.tokenExpires = time.Now().Add(time.Duration(tokenResp.ExpiresIn-60) * time.Second)

	return m.accessToken, nil
}

func (m *BaiduModel) doRequest(ctx context.Context, req *baiduChatRequest) (*baiduChatResponse, error) {
	token, err := m.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ep := fmt.Sprintf("/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/%s", m.model)
	u, err := url.JoinPath(m.baseURL, ep)
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}

	q := url.Values{}
	q.Set("access_token", token)
	u = u + "?" + q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

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
		return nil, fmt.Errorf("baidu API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var resp baiduChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *BaiduModel) doRequestRaw(ctx context.Context, req *baiduChatRequest) (io.ReadCloser, error) {
	token, err := m.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ep := fmt.Sprintf("/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/%s", m.model)
	u, err := url.JoinPath(m.baseURL, ep)
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}

	q := url.Values{}
	q.Set("access_token", token)
	u = u + "?" + q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("baidu API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	return httpResp.Body, nil
}

// baiduStreamReader reads streaming responses from Baidu.
type baiduStreamReader struct {
	reader    *bufio.Reader
	closer    io.Closer
	pendingFC *types.ToolCall // accumulating function_call fragments
	sawEnd    bool
	done      chan struct{}
	once      sync.Once
}

func newBaiduStreamReader(body io.ReadCloser) *baiduStreamReader {
	return &baiduStreamReader{
		reader: bufio.NewReader(body),
		closer: body,
		done:   make(chan struct{}),
	}
}

// Recv reads the next streamed message from the Baidu SSE stream, returning
// io.EOF at the end of the stream.
//
// Recv 从百度 SSE 流中读取下一条消息，流结束时返回 io.EOF。
func (r *baiduStreamReader) Recv() (*types.Message, error) {
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// A drop before is_end is a truncated stream — the pending
				// tool-call fragments would be silently lost.
				if r.sawEnd {
					return nil, io.EOF
				}
				return nil, fmt.Errorf("baidu stream ended prematurely before is_end")
			}
			return nil, fmt.Errorf("baidu stream read: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Baidu SSE format: "data: {...}" (space-less tolerated).
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var chunk baiduStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			msg := &types.Message{
				Role:    types.RoleAssistant,
				Content: chunk.Result,
			}

			// ERNIE streams function_call arguments in fragments across
			// chunks: accumulate by name, flush once at is_end (the old
			// code emitted one partial ToolCall per chunk with a fresh id).
			if chunk.FunctionCall != nil {
				fc := chunk.FunctionCall
				switch {
				case r.pendingFC == nil:
					r.pendingFC = &types.ToolCall{
						ID:   "call_baidu_" + fmt.Sprintf("%d", time.Now().UnixNano()),
						Type: "function",
						Function: types.ToolCallFunction{
							Name:      fc.Name,
							Arguments: fc.Arguments,
						},
					}
				case r.pendingFC.Function.Name == fc.Name:
					r.pendingFC.Function.Arguments += fc.Arguments
				default:
					// A second call started: flush the pending one.
					msg.ToolCalls = append(msg.ToolCalls, *r.pendingFC)
					r.pendingFC = &types.ToolCall{
						ID:   "call_baidu_" + fmt.Sprintf("%d", time.Now().UnixNano()),
						Type: "function",
						Function: types.ToolCallFunction{
							Name:      fc.Name,
							Arguments: fc.Arguments,
						},
					}
				}
			}

			if chunk.IsEnd {
				r.sawEnd = true
				if r.pendingFC != nil {
					msg.ToolCalls = append(msg.ToolCalls, *r.pendingFC)
					r.pendingFC = nil
					if msg.Content != "" || len(msg.ToolCalls) > 0 {
						return msg, nil
					}
				}
				return nil, io.EOF
			}

			if msg.Content != "" || len(msg.ToolCalls) > 0 {
				return msg, nil
			}
			continue
		}

		// Some Baidu streams might use plain JSON
		var chunk baiduStreamChunk
		if json.Unmarshal([]byte(line), &chunk) == nil && chunk.SentenceID > 0 || chunk.IsEnd {
			if chunk.IsEnd {
				r.sawEnd = true
				if r.pendingFC != nil {
					m := &types.Message{Role: types.RoleAssistant}
					m.ToolCalls = append(m.ToolCalls, *r.pendingFC)
					r.pendingFC = nil
					return m, nil
				}
				return nil, io.EOF
			}
			return &types.Message{
				Role:    types.RoleAssistant,
				Content: chunk.Result,
			}, nil
		}
	}
}

// Close closes the underlying stream body and signals Done.
//
// Close 关闭底层流并通知 Done。
func (r *baiduStreamReader) Close() error {
	err := r.closer.Close()
	r.once.Do(func() { close(r.done) })
	return err
}

// Done returns a channel that is closed when the stream is closed.
//
// Done 返回一个在流关闭时被关闭的 channel。
func (r *baiduStreamReader) Done() <-chan struct{} {
	return r.done
}

// messagesToBaidu converts gocel messages to Baidu ERNIE format.
func messagesToBaidu(msgs []*types.Message) []baiduMessage {
	result := make([]baiduMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		bm := baiduMessage{
			Role:    string(m.Role),
			Content: m.Content,
			Name:    m.ToolName,
		}

		// Map assistant tool calls - Baidu expects function_call in a separate field
		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			// Baidu doesn't natively support multiple tool calls in messages array;
			// skip mapping here - function_call is handled in response parsing
		}

		// Map tool results as "user" role content
		if m.Role == types.RoleTool {
			bm.Role = "user"
			bm.Content = fmt.Sprintf("Function result (%s): %s", m.ToolName, m.Content)
		}

		result = append(result, bm)
	}
	return result
}
