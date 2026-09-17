// Package llm provides ChatModel implementations for various LLM providers.
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

// OllamaConfig configures an Ollama chat model.
//
// OllamaConfig 配置 Ollama 聊天模型。
type OllamaConfig struct {
	// BaseURL is the Ollama server URL (default: http://localhost:11434).
	BaseURL string
	// Model is the model name (e.g. "llama3", "mistral", "codellama").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
	// KeepAlive duration for the model load in memory (0 = default).
	KeepAlive string
	// Options are Ollama-specific model options (temperature, top_p, etc.).
	Options map[string]any
}

// ollamaChatRequest is the request body for Ollama's /api/chat endpoint.
type ollamaChatRequest struct {
	Model     string          `json:"model"`
	Messages  []ollamaMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	Tools     []ollamaTool    `json:"tools,omitempty"`
	Options   map[string]any  `json:"options,omitempty"`
	KeepAlive string          `json:"keep_alive,omitempty"`
}

// ollamaMessage is a message in Ollama's format.
type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

// ollamaToolCall represents a tool call in Ollama's response.
type ollamaToolCall struct {
	Function ollamaToolCallFunction `json:"function"`
}

type ollamaToolCallFunction struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"` // JSON object, not string
}

// ollamaTool defines a tool for Ollama.
type ollamaTool struct {
	Type     string         `json:"type"`
	Function ollamaFunction `json:"function"`
}

type ollamaFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ollamaChatResponse is a single response from Ollama's /api/chat.
type ollamaChatResponse struct {
	Model           string         `json:"model"`
	CreatedAt       string         `json:"created_at"`
	Message         *ollamaMessage `json:"message,omitempty"`
	Done            bool           `json:"done"`
	DoneReason      string         `json:"done_reason,omitempty"`
	PromptEvalCount int            `json:"prompt_eval_count,omitempty"`
	EvalCount       int            `json:"eval_count,omitempty"`
	TotalDuration   int64          `json:"total_duration,omitempty"`
}

// OllamaModel implements kernel.ChatModel for Ollama.
//
// OllamaModel 为 Ollama 实现 kernel.ChatModel 接口。
type OllamaModel struct {
	baseURL   string
	model     string
	client    *http.Client
	options   map[string]any
	keepAlive string
}

// NewOllamaModel creates a new Ollama ChatModel.
// Default baseURL is http://localhost:11434.
//
// NewOllamaModel 创建一个新的 Ollama ChatModel。默认 baseURL 为
// http://localhost:11434。
func NewOllamaModel(model string, cfg *OllamaConfig) *OllamaModel {
	if cfg == nil {
		cfg = &OllamaConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
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

	return &OllamaModel{
		baseURL:   baseURL,
		model:     model,
		client:    client,
		options:   cfg.Options,
		keepAlive: cfg.KeepAlive,
	}
}

// Generate performs a synchronous chat completion via Ollama.
//
// Generate 通过 Ollama 执行同步的聊天补全。
func (m *OllamaModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("ollama generate: %w", err)
	}

	msg := ollamaToMessage(resp.Message)
	usage := &types.TokenUsage{
		PromptTokens:     resp.PromptEvalCount,
		CompletionTokens: resp.EvalCount,
		TotalTokens:      resp.PromptEvalCount + resp.EvalCount,
	}
	return msg, usage, nil
}

// Stream performs a streaming chat completion via Ollama.
// Each chunk contains incremental content.
//
// Stream 通过 Ollama 执行流式聊天补全，每个分块包含增量内容。
func (m *OllamaModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ollama stream: %w", err)
	}

	return newOllamaStreamReader(body), nil
}

// CountTokens returns an estimated token count for the given messages.
//
// CountTokens 返回给定消息的 token 数量估算。
func (m *OllamaModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

func (m *OllamaModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *ollamaChatRequest {
	req := &ollamaChatRequest{
		Model:     m.model,
		Messages:  messagesToOllama(messages),
		Stream:    stream,
		Options:   make(map[string]any),
		KeepAlive: m.keepAlive,
	}

	// Copy model-level options
	for k, v := range m.options {
		req.Options[k] = v
	}

	// Apply generation options
	if cfg.Temperature > 0 {
		req.Options["temperature"] = cfg.Temperature
	}
	if cfg.MaxTokens > 0 {
		req.Options["num_predict"] = cfg.MaxTokens
	}
	if cfg.TopP > 0 {
		req.Options["top_p"] = cfg.TopP
	}
	if len(cfg.Stop) > 0 {
		req.Options["stop"] = cfg.Stop
	}

	// Map tools
	for _, t := range cfg.Tools {
		req.Tools = append(req.Tools, ollamaTool{
			Type: "function",
			Function: ollamaFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	return req
}

func (m *OllamaModel) doRequest(ctx context.Context, req *ollamaChatRequest) (*ollamaChatResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.JoinPath(m.baseURL, "/api/chat")
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}
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

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("ollama API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var resp ollamaChatResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *OllamaModel) doRequestRaw(ctx context.Context, req *ollamaChatRequest) (io.ReadCloser, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.JoinPath(m.baseURL, "/api/chat")
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}
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
		return nil, fmt.Errorf("ollama API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	return httpResp.Body, nil
}

// ollamaStreamReader reads streaming responses from Ollama.
type ollamaStreamReader struct {
	reader  *bufio.Reader
	closer  io.Closer
	decoder *json.Decoder
	done    chan struct{}
	once    sync.Once
}

func newOllamaStreamReader(body io.ReadCloser) *ollamaStreamReader {
	return &ollamaStreamReader{
		reader:  bufio.NewReader(body),
		closer:  body,
		decoder: json.NewDecoder(body),
		done:    make(chan struct{}),
	}
}

// Recv reads the next streamed message from the Ollama stream, returning
// io.EOF at the end of the stream.
//
// Recv 从 Ollama 流中读取下一条消息，流结束时返回 io.EOF。
func (r *ollamaStreamReader) Recv() (*types.Message, error) {
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("ollama stream read: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var chunk ollamaChatResponse
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			return nil, fmt.Errorf("ollama stream decode: %w", err)
		}

		if chunk.Done {
			if chunk.Message != nil && len(chunk.Message.ToolCalls) > 0 {
				return ollamaToolCallsToMessage(chunk.Message.ToolCalls), nil
			}
			return nil, io.EOF
		}

		if chunk.Message != nil && len(chunk.Message.ToolCalls) > 0 {
			return ollamaToolCallsToMessage(chunk.Message.ToolCalls), nil
		}

		if chunk.Message == nil || chunk.Message.Content == "" {
			continue
		}

		return &types.Message{
			Role:    types.RoleAssistant,
			Content: chunk.Message.Content,
		}, nil
	}
}

// Close closes the underlying stream body and signals Done.
//
// Close 关闭底层流并通知 Done。
func (r *ollamaStreamReader) Close() error {
	err := r.closer.Close()
	r.once.Do(func() { close(r.done) })
	return err
}

// Done returns a channel that is closed when the stream is closed.
//
// Done 返回一个在流关闭时被关闭的 channel。
func (r *ollamaStreamReader) Done() <-chan struct{} {
	return r.done
}

// ── Message conversion ─────────────────────────────────────────────────

// messagesToOllama converts gocel messages to Ollama format.
func messagesToOllama(msgs []*types.Message) []ollamaMessage {
	result := make([]ollamaMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		om := ollamaMessage{
			Role:    string(m.Role),
			Content: m.Content,
		}

		// Handle multimodal content parts
		if m.IsMultimodal() {
			om.Content, om.Images = contentPartsToOllama(m.ContentParts)
		}

		// Map tool calls from assistant messages
		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				var args any
				// Try to parse as JSON object
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = tc.Function.Arguments
				}
				om.ToolCalls = append(om.ToolCalls, ollamaToolCall{
					Function: ollamaToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: args,
					},
				})
			}
		}

		result = append(result, om)
	}
	return result
}

// contentPartsToOllama converts ContentParts to Ollama text + images format.
// Ollama supports images as a separate base64 array alongside text content.
func contentPartsToOllama(parts []types.ContentPart) (string, []string) {
	var textBuilder strings.Builder
	var images []string

	for _, p := range parts {
		switch p.Type {
		case types.ContentTypeText:
			if textBuilder.Len() > 0 {
				textBuilder.WriteString("\n")
			}
			textBuilder.WriteString(p.Text)
		case types.ContentTypeImageData:
			if p.ImageData != nil && p.ImageData.Data != "" {
				images = append(images, p.ImageData.Data)
			}
		case types.ContentTypeImageURL:
			// Ollama doesn't natively support image URLs; embed as text reference
			if textBuilder.Len() > 0 {
				textBuilder.WriteString("\n")
			}
			textBuilder.WriteString("[Image: " + p.ImageURL + "]")
		default:
			if textBuilder.Len() > 0 {
				textBuilder.WriteString("\n")
			}
			textBuilder.WriteString(p.Text)
		}
	}

	text := textBuilder.String()
	if text == "" && len(images) > 0 {
		text = " "
	}
	return text, images
}

func ollamaToolCallsToMessage(tcs []ollamaToolCall) *types.Message {
	msg := &types.Message{
		Role: types.RoleAssistant,
	}
	baseID := fmt.Sprintf("call_ollama_%d", time.Now().UnixNano())
	for i, tc := range tcs {
		argsJSON := ""
		if tc.Function.Arguments != nil {
			if s, ok := tc.Function.Arguments.(string); ok {
				argsJSON = s
			} else if b, err := json.Marshal(tc.Function.Arguments); err == nil {
				argsJSON = string(b)
			}
		}
		msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
			ID:   fmt.Sprintf("%s_%d", baseID, i),
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: argsJSON,
			},
		})
	}
	return msg
}

// ollamaToMessage converts an Ollama response message to gocel format.
func ollamaToMessage(msg *ollamaMessage) *types.Message {
	if msg == nil {
		return nil
	}

	gocelMsg := &types.Message{
		Role:    types.Role(msg.Role),
		Content: msg.Content,
	}

	baseID := fmt.Sprintf("call_ollama_%d", time.Now().UnixNano())
	for i, tc := range msg.ToolCalls {
		argsJSON := ""
		if tc.Function.Arguments != nil {
			if s, ok := tc.Function.Arguments.(string); ok {
				argsJSON = s
			} else if b, err := json.Marshal(tc.Function.Arguments); err == nil {
				argsJSON = string(b)
			}
		}

		gocelMsg.ToolCalls = append(gocelMsg.ToolCalls, types.ToolCall{
			ID:   fmt.Sprintf("%s_%d", baseID, i),
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: argsJSON,
			},
		})
	}

	return gocelMsg
}
