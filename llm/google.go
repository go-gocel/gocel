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

// GeminiConfig configures a Google Gemini model.
//
// GeminiConfig 配置 Google Gemini 模型。
type GeminiConfig struct {
	// APIKey is the Google AI Studio API key.
	APIKey string
	// BaseURL defaults to https://generativelanguage.googleapis.com/v1beta.
	BaseURL string
	// Model is the model name (e.g. "gemini-2.5-flash", "gemini-2.5-pro").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
	// SafetySettings configures safety thresholds.
	SafetySettings []geminiSafetySetting
}

type geminiSafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// geminiRequest is the request for Gemini generateContent API.
type geminiRequest struct {
	Contents          []geminiContent        `json:"contents"`
	SystemInstruction *geminiContent         `json:"system_instruction,omitempty"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []geminiSafetySetting  `json:"safetySettings,omitempty"`
	Tools             []geminiTool           `json:"tools,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiBlob             `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiBlob struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiGenerationConfig struct {
	Temperature     float32  `json:"temperature,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	TopP            float32  `json:"topP,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiFunctionCall struct {
	Name string `json:"name"`
	Args any    `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string `json:"name"`
	Response any    `json:"response"`
}

// geminiResponse is the response from Gemini generateContent API.
type geminiResponse struct {
	Candidates     []geminiCandidate     `json:"candidates"`
	UsageMetadata  *geminiUsage          `json:"usageMetadata,omitempty"`
	PromptFeedback *geminiPromptFeedback `json:"promptFeedback,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type geminiPromptFeedback struct {
	BlockReason string `json:"blockReason"`
}

// geminiStreamChunk is a single chunk from Gemini streaming.
type geminiStreamChunk struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsage      `json:"usageMetadata,omitempty"`
}

// GeminiModel implements kernel.ChatModel for Google Gemini.
//
// GeminiModel 为 Google Gemini 实现 kernel.ChatModel 接口。
type GeminiModel struct {
	apiKey         string
	baseURL        string
	model          string
	client         *http.Client
	safetySettings []geminiSafetySetting
}

// NewGeminiModel creates a new Google Gemini ChatModel.
// Default baseURL is https://generativelanguage.googleapis.com/v1beta.
//
// NewGeminiModel 创建一个新的 Google Gemini ChatModel。默认 baseURL 为
// https://generativelanguage.googleapis.com/v1beta。
func NewGeminiModel(model, apiKey string, cfg *GeminiConfig) *GeminiModel {
	if cfg == nil {
		cfg = &GeminiConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
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

	return &GeminiModel{
		apiKey:         apiKey,
		baseURL:        baseURL,
		model:          model,
		client:         client,
		safetySettings: cfg.SafetySettings,
	}
}

// Generate performs a synchronous chat completion via Gemini.
//
// Generate 通过 Gemini 执行同步的聊天补全。
func (m *GeminiModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("gemini generate: %w", err)
	}

	if len(resp.Candidates) == 0 {
		if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
			return nil, nil, fmt.Errorf("gemini: prompt blocked, reason: %s", resp.PromptFeedback.BlockReason)
		}
		return nil, nil, fmt.Errorf("gemini: empty response (no candidates)")
	}

	msg := geminiToMessage(&resp.Candidates[0])
	var usage *types.TokenUsage
	if resp.UsageMetadata != nil {
		usage = &types.TokenUsage{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		}
	}
	return msg, usage, nil
}

// Stream performs a streaming chat completion via Gemini.
//
// Stream 通过 Gemini 执行流式聊天补全。
func (m *GeminiModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gemini stream: %w", err)
	}

	return newGeminiStreamReader(body), nil
}

// CountTokens returns an estimated token count for the given messages.
//
// CountTokens 返回给定消息的 token 数量估算。
func (m *GeminiModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

func (m *GeminiModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *geminiRequest {
	req := &geminiRequest{
		GenerationConfig: geminiGenerationConfig{},
	}

	var systemParts []string
	chatMessages := make([]*types.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == types.RoleSystem {
			systemParts = append(systemParts, msg.Content)
		} else {
			chatMessages = append(chatMessages, msg)
		}
	}

	req.Contents = messagesToGemini(chatMessages)

	if len(systemParts) > 0 {
		req.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: strings.Join(systemParts, "\n\n")}},
		}
	}

	if cfg.Temperature > 0 {
		req.GenerationConfig.Temperature = cfg.Temperature
	}
	if cfg.MaxTokens > 0 {
		req.GenerationConfig.MaxOutputTokens = cfg.MaxTokens
	}
	if cfg.TopP > 0 {
		req.GenerationConfig.TopP = cfg.TopP
	}
	if len(cfg.Stop) > 0 {
		req.GenerationConfig.StopSequences = cfg.Stop
	}

	// Map tools
	for _, t := range cfg.Tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		req.Tools = append(req.Tools, geminiTool{
			FunctionDeclarations: []geminiFunctionDecl{
				{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  schema,
				},
			},
		})
	}

	req.SafetySettings = m.safetySettings

	return req
}

func (m *GeminiModel) doRequest(ctx context.Context, req *geminiRequest) (*geminiResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ep := fmt.Sprintf("/models/%s:generateContent", m.model)
	u, err := url.JoinPath(m.baseURL, ep)
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}

	q := url.Values{}
	q.Set("key", m.apiKey)
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
		return nil, fmt.Errorf("gemini API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var resp geminiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *GeminiModel) doRequestRaw(ctx context.Context, req *geminiRequest) (io.ReadCloser, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ep := fmt.Sprintf("/models/%s:streamGenerateContent", m.model)
	u, err := url.JoinPath(m.baseURL, ep)
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}

	q := url.Values{}
	q.Set("key", m.apiKey)
	q.Set("alt", "sse")
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
		return nil, fmt.Errorf("gemini API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	return httpResp.Body, nil
}

// geminiStreamReader reads SSE streaming responses from Gemini.
type geminiStreamReader struct {
	reader *bufio.Reader
	closer io.Closer
	done   chan struct{}
	once   sync.Once
}

func newGeminiStreamReader(body io.ReadCloser) *geminiStreamReader {
	return &geminiStreamReader{
		reader: bufio.NewReader(body),
		closer: body,
		done:   make(chan struct{}),
	}
}

// Recv reads the next streamed message from the Gemini stream, returning
// io.EOF at the end of the stream.
//
// Recv 从 Gemini 流中读取下一条消息，流结束时返回 io.EOF。
func (r *geminiStreamReader) Recv() (*types.Message, error) {
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("gemini stream read: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// alt=sse frames chunks as "data: {...}" lines (C4: the old reader
		// parsed lines as plain JSON, skipped every frame, and delivered a
		// silent empty stream). Comment/event lines carry no content.
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			line = strings.TrimSpace(rest)
			if line == "" {
				continue
			}
		}

		var chunk geminiStreamChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			// Skip non-JSON lines
			continue
		}

		if len(chunk.Candidates) == 0 {
			continue
		}

		candidate := chunk.Candidates[0]
		msg := geminiCandidateToMessage(&candidate)

		// If there was usage metadata, attach it
		if chunk.UsageMetadata != nil && msg.Meta == nil {
			msg.Meta = make(map[string]any)
		}
		if chunk.UsageMetadata != nil {
			msg.Meta["usage"] = &types.TokenUsage{
				PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
				CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
			}
		}

		return msg, nil
	}
}

// Done returns a channel that is closed when the stream is closed.
//
// Done 返回一个在流关闭时被关闭的 channel。
func (r *geminiStreamReader) Done() <-chan struct{} {
	return r.done
}

// Close closes the stream, signals Done, and closes the underlying body if
// present.
//
// Close 关闭流、通知 Done，并在存在底层 body 时将其关闭。
func (r *geminiStreamReader) Close() error {
	r.once.Do(func() {
		close(r.done)
	})
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}

// messagesToGemini converts gocel messages to Gemini format.
func messagesToGemini(msgs []*types.Message) []geminiContent {
	result := make([]geminiContent, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}

		gc := geminiContent{
			Parts: []geminiPart{},
		}

		// Map role
		switch m.Role {
		case types.RoleUser:
			gc.Role = "user"
		case types.RoleAssistant:
			gc.Role = "model"
		case types.RoleTool:
			// Tool results are mapped as "function" role
			gc.Role = "function"
		default:
			gc.Role = string(m.Role)
		}

		// Handle multimodal content
		if m.IsMultimodal() {
			gc.Parts = contentPartsToGemini(m.ContentParts)
		} else if m.Content != "" {
			gc.Parts = append(gc.Parts, geminiPart{Text: m.Content})
		}

		// Map tool calls from assistant
		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				var args any
				json.Unmarshal([]byte(tc.Function.Arguments), &args)
				gc.Parts = append(gc.Parts, geminiPart{
					FunctionCall: &geminiFunctionCall{
						Name: tc.Function.Name,
						Args: args,
					},
				})
			}
		}

		// Map tool results
		if m.Role == types.RoleTool {
			gc.Parts = append(gc.Parts, geminiPart{
				FunctionResponse: &geminiFunctionResponse{
					Name: m.ToolName,
					Response: map[string]any{
						"result": m.Content,
					},
				},
			})
		}

		result = append(result, gc)
	}
	return result
}

// contentPartsToGemini converts ContentParts to Gemini parts.
func contentPartsToGemini(parts []types.ContentPart) []geminiPart {
	result := make([]geminiPart, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case types.ContentTypeText:
			result = append(result, geminiPart{Text: p.Text})
		case types.ContentTypeImageURL:
			result = append(result, geminiPart{
				InlineData: &geminiBlob{
					MIMEType: "image/jpeg",
					Data:     p.ImageURL,
				},
			})
		case types.ContentTypeImageData:
			mime := "image/png"
			if p.ImageData != nil && p.ImageData.MIMEType != "" {
				mime = p.ImageData.MIMEType
			}
			result = append(result, geminiPart{
				InlineData: &geminiBlob{
					MIMEType: mime,
					Data:     p.ImageData.Data,
				},
			})
		default:
			result = append(result, geminiPart{Text: p.Text})
		}
	}
	return result
}

// geminiToMessage converts a Gemini candidate to a gocel message.
func geminiToMessage(candidate *geminiCandidate) *types.Message {
	return geminiCandidateToMessage(candidate)
}

// geminiCandidateToMessage converts a Gemini candidate to a gocel message.
func geminiCandidateToMessage(candidate *geminiCandidate) *types.Message {
	msg := &types.Message{
		Role: types.RoleAssistant,
	}

	baseID := fmt.Sprintf("call_gemini_%d", time.Now().UnixNano())
	callIdx := 0
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			msg.Content += part.Text
		}
		if part.FunctionCall != nil {
			argsJSON := ""
			if part.FunctionCall.Args != nil {
				if b, err := json.Marshal(part.FunctionCall.Args); err == nil {
					argsJSON = string(b)
				}
			}
			msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
				ID:   fmt.Sprintf("%s_%d", baseID, callIdx),
				Type: "function",
				Function: types.ToolCallFunction{
					Name:      part.FunctionCall.Name,
					Arguments: argsJSON,
				},
			})
			callIdx++
		}
	}

	return msg
}
