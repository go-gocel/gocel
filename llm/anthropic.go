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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// AnthropicConfig configures an Anthropic Claude model.
//
// AnthropicConfig 配置 Anthropic Claude 模型。
type AnthropicConfig struct {
	// APIKey is the Anthropic API key.
	APIKey string
	// BaseURL defaults to https://api.anthropic.com/v1.
	BaseURL string
	// Model is the model name (e.g. "claude-sonnet-4-20250514", "claude-3-5-sonnet-20241022").
	Model string
	// Timeout is the HTTP client timeout (default: 5min).
	Timeout time.Duration
	// HTTPClient allows injecting a custom HTTP client.
	HTTPClient *http.Client
	// MaxTokens is the maximum tokens to generate (default: 4096).
	MaxTokens int
	// APIVersion is the Anthropic API version header (default: "2023-06-01").
	APIVersion string
}

// anthropicMessage is a message in Anthropic's format.
type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicImageSource is the source of an image in Anthropic format.
type anthropicImageSource struct {
	Type      string `json:"type"`                 // "base64" or "url"
	MediaType string `json:"media_type,omitempty"` // e.g. "image/png"
	Data      string `json:"data,omitempty"`       // base64 data
	URL       string `json:"url,omitempty"`        // URL
}

// anthropicBlock is a content block (text, image, tool_use, tool_result).
type anthropicBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	// PartialJSON carries the streamed tool-argument fragment of an
	// input_json_delta (Anthropic's real wire field — the old code only
	// read Delta.Text, so streamed tool arguments were always lost).
	PartialJSON string              `json:"partial_json,omitempty"`
	Source      *anthropicImageSource `json:"source,omitempty"`
	ID          string                `json:"id,omitempty"`
	Name        string                `json:"name,omitempty"`
	Input       json.RawMessage       `json:"input,omitempty"`
	ToolUseID   string                `json:"tool_use_id,omitempty"`
	Content     json.RawMessage       `json:"content,omitempty"`
	IsError     bool                  `json:"is_error,omitempty"`
}

// anthropicRequest is the request body for /v1/messages.
type anthropicRequest struct {
	Model         string             `json:"model"`
	Messages      []anthropicMessage `json:"messages"`
	System        string             `json:"system,omitempty"`
	MaxTokens     int                `json:"max_tokens"`
	Stream        bool               `json:"stream"`
	Temperature   float32            `json:"temperature,omitempty"`
	TopP          float32            `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Tools         []anthropicToolDef `json:"tools,omitempty"`
}

// anthropicToolDef defines a tool for Claude.
type anthropicToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// anthropicResponse is the response from /v1/messages.
type anthropicResponse struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      *anthropicUsage  `json:"usage"`
}

// anthropicUsage tracks token usage.
type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// anthropicStreamChunk is a single SSE event from Claude streaming.
type anthropicStreamChunk struct {
	Type         string             `json:"type"`
	Index        int                `json:"index,omitempty"`
	ContentBlock *anthropicBlock    `json:"content_block,omitempty"`
	Delta        *anthropicBlock    `json:"delta,omitempty"`
	Message      *anthropicResponse `json:"message,omitempty"`
	Usage        *anthropicUsage    `json:"usage,omitempty"`
}

// AnthropicModel implements kernel.ChatModel for Anthropic Claude.
//
// AnthropicModel 为 Anthropic Claude 实现 kernel.ChatModel 接口。
type AnthropicModel struct {
	apiKey     string
	baseURL    string
	model      string
	client     *http.Client
	maxTokens  int
	apiVersion string
}

// NewAnthropicModel creates a new Anthropic Claude ChatModel.
//
// NewAnthropicModel 创建一个新的 Anthropic Claude ChatModel。
func NewAnthropicModel(model, apiKey string, cfg *AnthropicConfig) *AnthropicModel {
	if cfg == nil {
		cfg = &AnthropicConfig{}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
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

	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = "2023-06-01"
	}

	return &AnthropicModel{
		apiKey:     apiKey,
		baseURL:    baseURL,
		model:      model,
		client:     client,
		maxTokens:  maxTokens,
		apiVersion: apiVersion,
	}
}

// Generate performs a synchronous chat completion via Anthropic.
//
// Generate 通过 Anthropic 执行同步的聊天补全。
func (m *AnthropicModel) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, false)
	resp, err := m.doRequest(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("anthropic generate: %w", err)
	}

	msg := anthropicToMessage(resp)
	// C3: a response without usage must not panic — the usage return is
	// simply nil and callers stay token-unaware for this call.
	var usage *types.TokenUsage
	if resp.Usage != nil {
		usage = &types.TokenUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
	}
	return msg, usage, nil
}

// Stream performs a streaming chat completion via Anthropic.
//
// Stream 通过 Anthropic 执行流式聊天补全。
func (m *AnthropicModel) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	cfg := &kernel.GenConfig{}
	cfg.Apply(opts)

	req := m.buildRequest(messages, cfg, true)

	body, err := m.doRequestRaw(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("anthropic stream: %w", err)
	}

	return newAnthropicStreamReader(body), nil
}

// CountTokens returns an estimated token count for the given messages.
//
// CountTokens 返回给定消息的 token 数量估算。
func (m *AnthropicModel) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	return kernel.DefaultCountTokens(messages), nil
}

func (m *AnthropicModel) buildRequest(messages []*types.Message, cfg *kernel.GenConfig, stream bool) *anthropicRequest {
	req := &anthropicRequest{
		Model:     m.model,
		MaxTokens: m.maxTokens,
		Stream:    stream,
	}

	// The GenOption MaxTokens must win over the constructor default — the
	// old code always sent m.maxTokens, silently dropping WithMaxTokens.
	if cfg.MaxTokens > 0 {
		req.MaxTokens = cfg.MaxTokens
	}

	if cfg.Temperature > 0 {
		req.Temperature = cfg.Temperature
	}
	if cfg.TopP > 0 {
		req.TopP = cfg.TopP
	}
	if len(cfg.Stop) > 0 {
		req.StopSequences = cfg.Stop
	}

	// Convert tools
	for _, t := range cfg.Tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		req.Tools = append(req.Tools, anthropicToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	// Convert messages: separate system from others
	userMessages := make([]*types.Message, 0, len(messages))
	systemContent := ""

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
			continue
		}
		userMessages = append(userMessages, msg)
	}

	req.System = systemContent
	req.Messages = messagesToAnthropic(userMessages)

	return req
}

func (m *AnthropicModel) doRequest(ctx context.Context, req *anthropicRequest) (*anthropicResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.JoinPath(m.baseURL, "/messages")
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", m.apiKey)
	httpReq.Header.Set("anthropic-version", m.apiVersion)

	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("anthropic API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var resp anthropicResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resp, nil
}

func (m *AnthropicModel) doRequestRaw(ctx context.Context, req *anthropicRequest) (io.ReadCloser, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	u, err := url.JoinPath(m.baseURL, "/messages")
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", m.apiKey)
	httpReq.Header.Set("anthropic-version", m.apiVersion)

	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("anthropic API error (status %d): %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	return httpResp.Body, nil
}

// ── Message conversion ─────────────────────────────────────────────────

// messagesToAnthropic converts gocel messages to Anthropic format.
func messagesToAnthropic(msgs []*types.Message) []anthropicMessage {
	result := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}

		// Map assistant messages with tool calls
		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			blocks := make([]anthropicBlock, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				argsRaw := json.RawMessage(tc.Function.Arguments)
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: argsRaw,
				})
			}
			result = append(result, anthropicMessage{Role: "assistant", Content: blocks})
			continue
		}

		// Map tool result messages
		if m.Role == types.RoleTool {
			contentRaw := json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%q}]`, m.Content))
			block := anthropicBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   contentRaw,
			}
			result = append(result, anthropicMessage{
				Role:    "user",
				Content: []anthropicBlock{block},
			})
			continue
		}

		// Handle multimodal content parts
		if m.IsMultimodal() {
			result = append(result, anthropicMessage{
				Role:    string(m.Role),
				Content: contentPartsToAnthropicBlocks(m.ContentParts),
			})
			continue
		}

		// Standard text messages
		result = append(result, anthropicMessage{
			Role:    string(m.Role),
			Content: []anthropicBlock{{Type: "text", Text: m.Content}},
		})
	}
	return result
}

// contentPartsToAnthropicBlocks converts ContentParts to Anthropic content blocks.
func contentPartsToAnthropicBlocks(parts []types.ContentPart) []anthropicBlock {
	blocks := make([]anthropicBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case types.ContentTypeText:
			blocks = append(blocks, anthropicBlock{
				Type: "text",
				Text: p.Text,
			})
		case types.ContentTypeImageURL:
			blocks = append(blocks, anthropicBlock{
				Type: "image",
				Source: &anthropicImageSource{
					Type: "url",
					URL:  p.ImageURL,
				},
			})
		case types.ContentTypeImageData:
			mime := "image/png"
			if p.ImageData != nil && p.ImageData.MIMEType != "" {
				mime = p.ImageData.MIMEType
			}
			blocks = append(blocks, anthropicBlock{
				Type: "image",
				Source: &anthropicImageSource{
					Type:      "base64",
					MediaType: mime,
					Data:      p.ImageData.Data,
				},
			})
		default:
			blocks = append(blocks, anthropicBlock{
				Type: "text",
				Text: p.Text,
			})
		}
	}
	return blocks
}

// anthropicToMessage converts an Anthropic response to a gocel message.
func anthropicToMessage(resp *anthropicResponse) *types.Message {
	msg := &types.Message{Role: types.Role(resp.Role)}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			msg.Content += block.Text
		case "tool_use":
			argsStr := ""
			if block.Input != nil {
				argsStr = string(block.Input)
			}
			msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: types.ToolCallFunction{
					Name:      block.Name,
					Arguments: argsStr,
				},
			})
		}
	}
	return msg
}

// ── Streaming ──────────────────────────────────────────────────────────

// anthropicStreamReader reads SSE streaming responses from Anthropic.
type anthropicStreamReader struct {
	reader       *bufio.Reader
	closer       io.Closer
	accText      strings.Builder
	accTCs       map[int]*types.ToolCall // index-keyed (parallel calls)
	pendingUsage *types.TokenUsage
	sawStop      bool
	done         chan struct{}
	once         sync.Once
}

func newAnthropicStreamReader(body io.ReadCloser) *anthropicStreamReader {
	return &anthropicStreamReader{
		reader: bufio.NewReader(body),
		closer: body,
		done:   make(chan struct{}),
	}
}

// Recv reads the next streamed message from the Anthropic SSE stream,
// returning io.EOF at the end of the stream.
//
// Recv 从 Anthropic SSE 流中读取下一条消息，流结束时返回 io.EOF。
func (r *anthropicStreamReader) Recv() (*types.Message, error) {
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// A drop before message_stop is a truncated stream, not a
				// clean end — pending tool calls would be silently lost.
				if r.sawStop {
					return nil, io.EOF
				}
				return nil, fmt.Errorf("anthropic stream ended prematurely before message_stop")
			}
			return nil, fmt.Errorf("anthropic stream read: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// SSE format: "event: ..." then "data: {...}"
		if strings.HasPrefix(line, "event: ") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		var chunk anthropicStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("anthropic stream decode: %w", err)
		}

		switch chunk.Type {
		case "content_block_start":
			if chunk.ContentBlock != nil && chunk.ContentBlock.Type == "tool_use" {
				args := ""
				if chunk.ContentBlock.Input != nil && string(chunk.ContentBlock.Input) != "{}" {
					args = string(chunk.ContentBlock.Input)
				}
				if r.accTCs == nil {
					r.accTCs = make(map[int]*types.ToolCall)
				}
				r.accTCs[chunk.Index] = &types.ToolCall{
					ID:   chunk.ContentBlock.ID,
					Type: "function",
					Function: types.ToolCallFunction{
						Name:      chunk.ContentBlock.Name,
						Arguments: args,
					},
				}
			}

		case "content_block_delta":
			if chunk.Delta != nil {
				if chunk.Delta.Type == "text_delta" && chunk.Delta.Text != "" {
					r.accText.WriteString(chunk.Delta.Text)
					return &types.Message{
						Role:    types.RoleAssistant,
						Content: chunk.Delta.Text,
					}, nil
				}
				if chunk.Delta.Type == "input_json_delta" && chunk.Delta.PartialJSON != "" {
					if r.accTCs == nil {
						r.accTCs = make(map[int]*types.ToolCall)
					}
					tc := r.accTCs[chunk.Index]
					if tc == nil {
						tc = &types.ToolCall{Type: "function"}
						r.accTCs[chunk.Index] = tc
					}
					if tc.ID == "" && chunk.ContentBlock != nil {
						tc.ID = chunk.ContentBlock.ID
						tc.Function.Name = chunk.ContentBlock.Name
					}
					tc.Function.Arguments += chunk.Delta.PartialJSON
				}
			}

		case "message_delta":
			if chunk.Usage != nil {
				if r.pendingUsage == nil {
					r.pendingUsage = &types.TokenUsage{}
				}
				r.pendingUsage.CompletionTokens = chunk.Usage.OutputTokens
				r.pendingUsage.TotalTokens = r.pendingUsage.PromptTokens + r.pendingUsage.CompletionTokens
			}

		case "message_stop":
			r.sawStop = true
			msg := &types.Message{
				Role: types.RoleAssistant,
			}
			if text := r.accText.String(); text != "" {
				msg.Content = text
			}
			if len(r.accTCs) > 0 {
				idx := make([]int, 0, len(r.accTCs))
				for k := range r.accTCs {
					idx = append(idx, k)
				}
				sort.Ints(idx)
				for _, k := range idx {
					msg.ToolCalls = append(msg.ToolCalls, *r.accTCs[k])
				}
			}
			if r.pendingUsage != nil {
				msg.Meta = map[string]any{"usage": r.pendingUsage}
				r.pendingUsage = nil
			}
			r.accText.Reset()
			r.accTCs = nil
			if len(msg.ToolCalls) > 0 || msg.Meta != nil || msg.Content != "" {
				return msg, nil
			}
			return nil, io.EOF

		case "message_start":
			if chunk.Message != nil {
				if chunk.Message.Usage != nil {
					r.pendingUsage = &types.TokenUsage{
						PromptTokens: chunk.Message.Usage.InputTokens,
					}
				}
				// Check for initial content blocks
				for _, block := range chunk.Message.Content {
					if block.Type == "tool_use" {
						args := ""
						if block.Input != nil && string(block.Input) != "{}" {
							args = string(block.Input)
						}
						if r.accTCs == nil {
							r.accTCs = make(map[int]*types.ToolCall)
						}
						r.accTCs[len(r.accTCs)] = &types.ToolCall{
							ID:   block.ID,
							Type: "function",
							Function: types.ToolCallFunction{
								Name:      block.Name,
								Arguments: args,
							},
						}
					}
				}
			}
		}
	}
}

// Close closes the underlying stream body and signals Done.
//
// Close 关闭底层流并通知 Done。
func (r *anthropicStreamReader) Close() error {
	err := r.closer.Close()
	r.once.Do(func() { close(r.done) })
	return err
}

// Done returns a channel that is closed when the stream is closed.
//
// Done 返回一个在流关闭时被关闭的 channel。
func (r *anthropicStreamReader) Done() <-chan struct{} {
	return r.done
}
