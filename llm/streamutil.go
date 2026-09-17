package llm

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/types"
)

// sseDelta is the common shape of an OpenAI-style stream delta shared by
// the openai / deepseek / moonshot adapters (C7: one state machine, three
// providers).
type sseDelta interface {
	sseContent() string
	sseReasoning() string
	sseToolCalls() []openaiToolCall
}

func (m openaiMessage) sseContent() string {
	s, _ := m.Content.(string)
	return s
}
func (m openaiMessage) sseReasoning() string           { return m.ReasoningContent }
func (m openaiMessage) sseToolCalls() []openaiToolCall { return m.ToolCalls }

func (m deepseekMessage) sseContent() string {
	s, _ := m.Content.(string)
	return s
}
func (m deepseekMessage) sseReasoning() string           { return m.ReasoningContent }
func (m deepseekMessage) sseToolCalls() []openaiToolCall { return m.ToolCalls }

func (m moonshotRespMsg) sseContent() string {
	s, _ := m.Content.(string)
	return s
}
func (m moonshotRespMsg) sseReasoning() string           { return m.ReasoningContent }
func (m moonshotRespMsg) sseToolCalls() []openaiToolCall { return m.ToolCalls }

// toolCallAccumulator merges index-keyed tool-call chunks (C2/C7): sparse
// indices survive, chunked arguments concatenate, and flush is
// deterministic (index order).
type toolCallAccumulator struct {
	acc        map[int]*types.ToolCall
	hasPending bool
}

func (a *toolCallAccumulator) add(tcs []openaiToolCall) {
	if a.acc == nil {
		a.acc = make(map[int]*types.ToolCall)
	}
	for _, tc := range tcs {
		idx := 0
		if tc.Index != nil {
			idx = *tc.Index
		}
		existing, ok := a.acc[idx]
		if !ok {
			existing = &types.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: types.ToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
			a.acc[idx] = existing
		} else {
			existing.Function.Arguments += tc.Function.Arguments
			if existing.ID == "" {
				existing.ID = tc.ID
			}
			if existing.Function.Name == "" {
				existing.Function.Name = tc.Function.Name
			}
		}
	}
	a.hasPending = true
}

// flush builds the assistant tool-call message (attaching the pending
// usage when non-nil) and resets the accumulator; nil when empty.
func (a *toolCallAccumulator) flush(usage *types.TokenUsage) *types.Message {
	if !a.hasPending {
		return nil
	}
	tcs := sortedToolCalls(a.acc)
	a.acc = make(map[int]*types.ToolCall)
	a.hasPending = false
	msg := &types.Message{Role: types.RoleAssistant, ToolCalls: tcs}
	if usage != nil {
		msg.Meta = map[string]any{"usage": usage}
	}
	return msg
}

// openaiStyleStreamReader drives the shared SSE state machine for the
// OpenAI-compatible family (C7): tool-call accumulation, usage delivery
// (including the final usage-only chunk), and text/reasoning sequencing are
// shared; provider differences (chunk shape, decode tolerance) adapt via
// the decode closure.
type openaiStyleStreamReader struct {
	reader *bufio.Reader
	closer io.Closer
	done   chan struct{}
	once   sync.Once

	acc          toolCallAccumulator
	pendingUsage *types.TokenUsage
	pendingText  string
	// sawTerminator records a proper stream end (finish reason or [DONE]):
	// EOF before it is a truncated stream, never a clean end.
	sawTerminator bool

	// decode parses one data payload: the delta, the finish reason, the
	// chunk usage. ok=false skips the chunk (a provider tolerated it);
	// err fails the stream.
	decode func(data []byte) (delta sseDelta, finish string, usage *openaiUsage, ok bool, err error)
	// errWrap decorates read/decode errors with the provider name.
	errWrap func(err error) error
}

func newOpenAIStyleStreamReader(body io.ReadCloser, decode func([]byte) (sseDelta, string, *openaiUsage, bool, error), errWrap func(error) error) *openaiStyleStreamReader {
	return &openaiStyleStreamReader{
		reader:  bufio.NewReader(body),
		closer:  body,
		done:    make(chan struct{}),
		decode:  decode,
		errWrap: errWrap,
	}
}

func (r *openaiStyleStreamReader) usageFrom(u *openaiUsage) *types.TokenUsage {
	if u == nil {
		return nil
	}
	return &types.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// takeUsageToken consumes and returns the pending usage.
func (r *openaiStyleStreamReader) takeUsageToken() *types.TokenUsage {
	u := r.pendingUsage
	r.pendingUsage = nil
	return u
}

// takeUsage returns a usage-only message when one is pending, consuming it.
func (r *openaiStyleStreamReader) takeUsage() *types.Message {
	if u := r.takeUsageToken(); u != nil {
		return usageOnlyMessage(u)
	}
	return nil
}

// flushPending returns a flushed tool-call message, consuming the pending
// usage ONLY when a flush actually happens (a usage held for a pure-text
// run must survive until finish/[DONE]).
func (r *openaiStyleStreamReader) flushPending() *types.Message {
	u := r.pendingUsage
	msg := r.acc.flush(u)
	if msg != nil {
		r.pendingUsage = nil
	}
	return msg
}

// Recv returns the next stream message (text, reasoning, tool call, or
// usage), or io.EOF at the end of a clean stream.
//
// Recv 返回下一条流式消息（文本、推理、工具调用或用量）；流正常结束时
// 返回 io.EOF。
func (r *openaiStyleStreamReader) Recv() (*types.Message, error) {
	// Buffered text from a previous flush goes out first.
	if r.pendingText != "" {
		content := r.pendingText
		r.pendingText = ""
		msg := &types.Message{Role: types.RoleAssistant, Content: content}
		if u := r.takeUsageToken(); u != nil {
			msg.Meta = map[string]any{"usage": u}
		}
		return msg, nil
	}

	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// A drop before finish/[DONE] truncates the stream — the
				// pending tool calls and text must not masquerade as a
				// clean end.
				if r.sawTerminator {
					return nil, io.EOF
				}
				return nil, r.errWrap(fmt.Errorf("stream ended prematurely before finish/[DONE]"))
			}
			return nil, r.errWrap(fmt.Errorf("stream read: %w", err))
		}
		line = strings.TrimSpace(line)
		// Tolerate "data:{...}" (space-less) — some gateways emit it.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			r.sawTerminator = true
			if msg := r.flushPending(); msg != nil {
				return msg, nil
			}
			if u := r.takeUsage(); u != nil {
				return u, nil
			}
			return nil, io.EOF
		}

		delta, finish, usage, ok, err := r.decode([]byte(data))
		if err != nil {
			return nil, r.errWrap(err)
		}
		if !ok {
			continue
		}
		if u := r.usageFrom(usage); u != nil {
			r.pendingUsage = u
		}
		if finish != "" {
			r.sawTerminator = true
			if msg := r.flushPending(); msg != nil {
				return msg, nil
			}
			if u := r.takeUsage(); u != nil {
				return u, nil
			}
			return nil, io.EOF
		}
		if delta == nil {
			continue
		}
		if rc := delta.sseReasoning(); rc != "" {
			return &types.Message{Role: types.RoleAssistant, ReasoningContent: rc}, nil
		}
		if tcs := delta.sseToolCalls(); len(tcs) > 0 {
			r.acc.add(tcs)
			// Text carried alongside a tool-call delta must not be lost.
			if text := delta.sseContent(); text != "" {
				r.pendingText += text
			}
			continue
		}
		if text := delta.sseContent(); text != "" {
			if msg := r.flushPending(); msg != nil {
				r.pendingText = text
				return msg, nil
			}
			return &types.Message{Role: types.RoleAssistant, Content: text}, nil
		}
	}
}

// Done returns a channel that is closed when the stream is closed.
//
// Done 返回一个在流关闭时被关闭的 channel。
func (r *openaiStyleStreamReader) Done() <-chan struct{} { return r.done }

// Close closes the stream and releases the underlying body.
//
// Close 关闭流并释放底层响应体。
func (r *openaiStyleStreamReader) Close() error {
	r.once.Do(func() { close(r.done) })
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}

// sortedToolCalls flattens the index-keyed tool-call accumulator in index
// order (C2): sparse indices survive and parallel calls come out
// deterministically, no matter which order the provider streamed them.
//
// sortedToolCalls 按索引顺序展平索引键控的工具调用累积器（C2）：稀疏索引
// 不丢失，并行调用确定性输出，与提供商流式顺序无关。
func sortedToolCalls(acc map[int]*types.ToolCall) []types.ToolCall {
	keys := make([]int, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	tcs := make([]types.ToolCall, 0, len(keys))
	for _, k := range keys {
		tcs = append(tcs, *acc[k])
	}
	return tcs
}

// usageOnlyMessage carries the stream's final usage when no other message
// would: a pure-text run's usage-only chunk must reach the consumer instead
// of being dropped at finish/[DONE] (C1).
//
// usageOnlyMessage 在其他消息都不会携带时承载流式最终用量：纯文本运行的
// usage-only 块必须到达消费方，而不是在 finish/[DONE] 被丢弃（C1）。
func usageOnlyMessage(u *types.TokenUsage) *types.Message {
	return &types.Message{Role: types.RoleAssistant, Meta: map[string]any{"usage": u}}
}
