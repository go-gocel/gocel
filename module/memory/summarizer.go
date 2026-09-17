package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Summarizer generates a concise summary of a conversation interaction.
// The summary is used to preserve key information when the interaction
// is compressed or evicted from the active window.
//
// Summarizer 为一次对话交互生成简明摘要。当交互被压缩或从活动窗口
// 驱逐时，摘要用于保留关键信息。
type Summarizer interface {
	// Summarize produces a condensed textual summary of the given messages.
	// The summary should capture: user intent, key decisions, tool results,
	// and any important conclusions.
	Summarize(ctx context.Context, msgs []*types.Message) (string, error)
}

// NoopSummarizer returns an empty string — disables summarization.
// NoopSummarizer 返回空字符串——禁用摘要功能。
type NoopSummarizer struct{}

// Summarize returns empty string (no-op).
// Summarize 返回空字符串（无操作）。
func (n *NoopSummarizer) Summarize(_ context.Context, _ []*types.Message) (string, error) {
	return "", nil
}

// LLMSummarizer uses a ChatModel to generate summaries via LLM.
// LLMSummarizer 使用 ChatModel 通过 LLM 生成摘要。
type LLMSummarizer struct {
	mu     sync.RWMutex
	model  kernel.ChatModel
	system string // system prompt for the summarizer
	opts   []kernel.GenOption
}

// NewLLMSummarizer creates an LLM-powered summarizer.
// model: the ChatModel to use for summarization calls — the contract the
// summarizer actually needs (no naked assertion at the call site).
// opts: optional generation options (e.g., temperature, max tokens).
//
// NewLLMSummarizer 创建由 LLM 驱动的摘要器。model 是摘要调用使用的
// ChatModel；opts 是可选的生成选项（如 temperature、max tokens）。
func NewLLMSummarizer(model kernel.ChatModel, opts ...kernel.GenOption) *LLMSummarizer {
	return &LLMSummarizer{
		model: model,
		system: `You are a conversation summarizer. Your task is to produce a concise, information-dense summary of a conversation turn between a user and an AI assistant.

The summary MUST include:
1. What the user asked or requested
2. What tools were called and their key results
3. Important decisions, conclusions, or data points
4. Any follow-up actions or pending items

Guidelines:
- Keep the summary under 200 tokens
- Focus on factual information, not conversational filler
- Use bullet points for multiple items
- Preserve specific numbers, names, and IDs
- Omit pleasantries and meta-commentary`,
	}
}

// WithSummarizerSystem sets a custom system prompt for summarization.
// WithSummarizerSystem 为摘要设置自定义系统提示词。
func (s *LLMSummarizer) WithSummarizerSystem(prompt string) *LLMSummarizer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.system = prompt
	return s
}

// Summarize generates a summary using the LLM.
// Summarize 使用 LLM 生成摘要。
func (s *LLMSummarizer) Summarize(ctx context.Context, msgs []*types.Message) (string, error) {
	s.mu.RLock()
	model := s.model
	sysPrompt := s.system
	opts := s.opts
	s.mu.RUnlock()

	if model == nil {
		return "", fmt.Errorf("LLMSummarizer: model is nil")
	}

	if len(msgs) == 0 {
		return "", nil
	}

	// Build the conversation to summarize
	summaryMsgs := []*types.Message{
		types.NewSystemMessage(sysPrompt),
	}
	summaryMsgs = append(summaryMsgs, msgs...)

	resp, _, err := s.model.Generate(ctx, summaryMsgs, opts...)
	if err != nil {
		return "", fmt.Errorf("LLMSummarizer: generate failed: %w", err)
	}
	if resp == nil {
		return "", nil
	}

	return strings.TrimSpace(resp.Content), nil
}

// RecursiveSummarizer creates a hierarchical summary by summarizing groups
// of interactions, then summarizing those summaries. Useful for very long
// conversation histories.
//
// RecursiveSummarizer 先对交互分组摘要，再对摘要进行摘要，生成分层摘要，
// 适用于非常长的对话历史。
type RecursiveSummarizer struct {
	inner     Summarizer
	batchSize int // number of summaries to combine at each level (default 5)
}

// NewRecursiveSummarizer creates a recursive summarizer that wraps another.
// NewRecursiveSummarizer 创建包装另一个摘要器的递归摘要器。
func NewRecursiveSummarizer(inner Summarizer, batchSize int) *RecursiveSummarizer {
	if batchSize <= 1 {
		batchSize = 5
	}
	return &RecursiveSummarizer{
		inner:     inner,
		batchSize: batchSize,
	}
}

// Summarize generates a summary. If msgs is short, delegates to inner.
// For long conversations, splits into batches, summarizes each batch,
// then summarizes the batch summaries recursively.
//
// Summarize 生成摘要。消息较短时委托给 inner；长对话则分批摘要，再递归
// 摘要各批的摘要。
func (r *RecursiveSummarizer) Summarize(ctx context.Context, msgs []*types.Message) (string, error) {
	if len(msgs) <= 20 {
		return r.inner.Summarize(ctx, msgs)
	}

	// Split into batches
	var batchSummaries []string
	for i := 0; i < len(msgs); i += r.batchSize {
		end := i + r.batchSize
		if end > len(msgs) {
			end = len(msgs)
		}
		batch := msgs[i:end]
		summary, err := r.inner.Summarize(ctx, batch)
		if err != nil {
			return "", fmt.Errorf("RecursiveSummarizer: batch %d failed: %w", i/r.batchSize, err)
		}
		if summary != "" {
			batchSummaries = append(batchSummaries, summary)
		}
	}

	if len(batchSummaries) == 0 {
		return "", nil
	}

	// If only one batch summary, return it directly
	if len(batchSummaries) == 1 {
		return batchSummaries[0], nil
	}

	// Summarize the batch summaries
	metaMsgs := []*types.Message{
		types.NewSystemMessage("Below are summaries of different parts of a conversation. Combine them into a single coherent summary."),
	}
	for i, s := range batchSummaries {
		metaMsgs = append(metaMsgs, types.NewUserMessage(fmt.Sprintf("Part %d summary:\n%s", i+1, s)))
	}

	return r.inner.Summarize(ctx, metaMsgs)
}

// AsGuardSummarizer adapts a memory Summarizer to the guard module's
// summarizer signature — the single-source-of-truth seam: memory owns the
// summarization expertise (LLM/recursive/noop), and module/guard's summary
// compaction consumes it without duplicating the logic.
//
// AsGuardSummarizer 把 memory 的 Summarizer 适配为 guard 模块的摘要器
// 签名——单一来源缝：memory 拥有摘要专业能力（LLM/递归/noop），
// module/guard 的摘要压缩消费它而不复制逻辑。
func AsGuardSummarizer(s Summarizer) func(ctx context.Context, msgs []*types.Message) (string, error) {
	return func(ctx context.Context, msgs []*types.Message) (string, error) {
		return s.Summarize(ctx, msgs)
	}
}
