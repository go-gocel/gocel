// Package memory provides MemoryModule for conversation history management.
//
// MemoryModule trims or summarizes long conversations to fit within context limits.
// It can discard old messages, summarize them via a secondary model, or both.
// Configure via MemoryConfig.
//
// 记忆管理模块。MemoryModule 通过裁剪或摘要旧消息来控制上下文长度，
// 支持丢弃旧消息、用辅助模型生成摘要、或两者组合。通过 MemoryConfig 配置。
package memory

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// MemoryModule provides three-tier cross-turn conversation memory via hooks.
//
// Tier 1 — Working Memory: raw interactions of the current session.
// Tier 2 — Summary Memory:  hierarchical LLM summaries (MemoryStore).
// Tier 3 — Archive Memory:  cross-session persisted summaries (ArchiveMemory).
//
// A TokenBudget allocates the total character budget among the three tiers.
//
// Hooks:
//
//	HookAfterMessages:  inject memory context (all three tiers) into system prompt
//	HookAfterAgentRun:  save current interaction, optionally summarize and archive
//
// MemoryModule 提供三层跨轮次对话记忆：工作记忆（当前会话的原始交互）、
// 摘要记忆（MemoryStore 层级化 LLM 摘要）与归档记忆（跨会话持久化摘要）；
// TokenBudget 负责把总字符预算分配到三个层级。
type MemoryModule struct {
	mu  sync.RWMutex
	cfg MemoryConfig

	// summarizer generates summaries of interactions (optional, Tier 2).
	summarizer Summarizer

	// scorer assigns importance scores for eviction decisions (optional).
	scorer Scorer

	// store maintains hierarchical summaries (Tier 2, used with summarizer).
	store *MemoryStore

	// archiveMem maintains cross-session summaries (Tier 3, optional).
	archiveMem *ArchiveMemory

	// budget allocates total token budget among the three tiers.
	budget *TokenBudget

	// decay drives the memory decay and forgetting mechanism (optional).
	decay *MemoryDecay

	// interactionCount tracks total interactions saved for archive interval logic.
	interactionCount int

	// stored interactions, ordered by recency (newest last). Tier 1.
	interactions []interaction

	// lastSavedMsgCount tracks how many messages were in the input,
	// so we only save the delta on HookAfterAgentRun.
	lastSavedMsgCount int
}

// interaction stores a single user-agent exchange.
// Only the user message text and optional summary are retained,
// not the full message list, to minimize memory usage.
type interaction struct {
	userMsg    string  // last user message text
	summary    string  // LLM-generated summary (optional)
	importance float64 // cached importance score, updated on save
}

// NewMemoryModule creates a memory module with defaults.
//
// NewMemoryModule 使用默认配置创建记忆模块。
func NewMemoryModule() *MemoryModule {
	return &MemoryModule{
		cfg:    DefaultMemoryConfig(),
		budget: NewTokenBudget(DefaultBudgetConfig()),
	}
}

// NewMemoryModuleWith creates a memory module with custom config.
//
// NewMemoryModuleWith 使用自定义配置创建记忆模块。
func NewMemoryModuleWith(cfg MemoryConfig) *MemoryModule {
	m := NewMemoryModule()

	if cfg.MaxInteractions > 0 {
		m.cfg.MaxInteractions = cfg.MaxInteractions
	}
	if cfg.TruncateMessageLen > 0 {
		m.cfg.TruncateMessageLen = cfg.TruncateMessageLen
	}
	if cfg.MaxTotalChars > 0 {
		m.cfg.MaxTotalChars = cfg.MaxTotalChars
	}
	if cfg.StoreGroupSize > 1 {
		m.cfg.StoreGroupSize = cfg.StoreGroupSize
	}
	if cfg.MaxArchiveSessions > 0 {
		m.cfg.MaxArchiveSessions = cfg.MaxArchiveSessions
	}
	if cfg.ArchiveSaveInterval > 0 {
		m.cfg.ArchiveSaveInterval = cfg.ArchiveSaveInterval
	}
	m.cfg.EnableSummarization = cfg.EnableSummarization
	m.cfg.EnableImportanceScoring = cfg.EnableImportanceScoring
	m.cfg.ArchivePath = cfg.ArchivePath

	// Initialize archive if path is set
	if cfg.ArchivePath != "" {
		m.archiveMem = NewArchiveMemory(cfg.ArchivePath, cfg.MaxArchiveSessions)
	}

	// Budget
	bCfg := cfg.Budget
	bCfg.MinReserve = maxInt(bCfg.MinReserve, 200)
	m.budget = NewTokenBudget(bCfg)

	// Initialize decay if config has it enabled
	if cfg.Decay.Enabled {
		m.decay = NewMemoryDecay(cfg.Decay)
	}

	return m
}

// WithSummarizer sets the summarizer for LLM-powered summary generation (Tier 2).
// When set, each interaction will be summarized after it is saved.
//
// WithSummarizer 设置用于 LLM 摘要生成（第 2 层）的摘要器；设置后每条交互在
// 保存后都会被摘要。
func (m *MemoryModule) WithSummarizer(s Summarizer) *MemoryModule {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.summarizer = s
	if m.store == nil && m.cfg.EnableSummarization {
		m.store = NewMemoryStore(m.cfg.StoreGroupSize)
	}
	return m
}

// WithScorer sets the scorer for importance-based interaction retention.
//
// WithScorer 设置用于按重要性保留交互的评分器。
func (m *MemoryModule) WithScorer(s Scorer) *MemoryModule {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scorer = s
	return m
}

// WithArchive enables cross-session archive memory (Tier 3) backed by the given file path.
//
// WithArchive 启用由给定文件路径支撑的跨会话归档记忆（第 3 层）。
func (m *MemoryModule) WithArchive(filePath string) *MemoryModule {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.ArchivePath = filePath
	m.archiveMem = NewArchiveMemory(filePath, m.cfg.MaxArchiveSessions)
	return m
}

// WithBudget sets a custom token budget configuration.
//
// WithBudget 设置自定义 token 预算配置。
func (m *MemoryModule) WithBudget(cfg BudgetConfig) *MemoryModule {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.MinReserve <= 0 {
		cfg.MinReserve = 200
	}
	m.cfg.Budget = cfg
	m.budget = NewTokenBudget(cfg)
	return m
}

// WithDecay enables memory decay with the given configuration.
//
// WithDecay 使用给定配置启用记忆衰减。
func (m *MemoryModule) WithDecay(cfg DecayConfig) *MemoryModule {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Decay = cfg
	m.decay = NewMemoryDecay(cfg)
	return m
}

// Register registers the memory hooks on the runtime and loads any archive.
//
// Register 在运行时上注册记忆钩子并加载任何归档。
func (m *MemoryModule) Register(rt kernel.HookRegistrar) {
	// Load archive (non-fatal on failure)
	m.mu.RLock()
	archive := m.archiveMem
	m.mu.RUnlock()

	if archive != nil {
		_ = archive.Load()
	}

	rt.OnMessagesBuilt(m.onMessagesBuilt)
	rt.OnAgentEnd(m.onAgentEnd)
}

func (m *MemoryModule) onMessagesBuilt(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
	if len(msgs) == 0 {
		return ctx, msgs, nil
	}

	ctxStr := m.buildSystemPrompt(ctx)

	// Record message count regardless of whether memory context is injected (P0 fix: must
	// be set before the early return so onAfterAgentRun can compute the delta correctly).
	m.mu.Lock()
	m.lastSavedMsgCount = len(msgs)
	m.mu.Unlock()

	if ctxStr == "" {
		return ctx, msgs, nil
	}

	// 追加记忆上下文，由 FireMessagesBuilt 统一合并进 system（无需 Clone）。
	injected := append(msgs, types.NewSystemMessage(ctxStr))

	m.mu.Lock()
	m.lastSavedMsgCount = len(injected)
	m.mu.Unlock()

	return ctx, injected, nil
}

func (m *MemoryModule) onAgentEnd(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
	if info == nil || info.AllMsgs == nil {
		return ctx, info, nil
	}

	// Only save the delta — messages added during this run.
	m.mu.RLock()
	lastCount := m.lastSavedMsgCount
	m.mu.RUnlock()

	if lastCount <= 0 || lastCount >= len(info.AllMsgs) {
		return ctx, info, nil
	}
	newMsgs := info.AllMsgs[lastCount:]

	// Generate summary if summarizer is configured.
	var summary string
	m.mu.RLock()
	summarizer := m.summarizer
	m.mu.RUnlock()
	if summarizer != nil {
		s, err := summarizer.Summarize(ctx, newMsgs)
		if err == nil {
			summary = s
		} else {
			log.Printf("[memory] summarizer error: %v", err) // P5: log instead of swallow
		}
	}

	m.mu.Lock()
	m.interactionCount++
	interactionCount := m.interactionCount
	m.mu.Unlock()

	m.saveInteraction(ctx, info.AllMsgs, summary)

	// Archive and decay logic (P6: extracted shared method).
	m.doArchiveAndDecay(interactionCount, summary)
	return ctx, info, nil
}

// doArchiveAndDecay handles archiving and decay sweep — called from onAfterAgentRun
// and also from SaveInteraction so external callers get the same behavior (P6).
func (m *MemoryModule) doArchiveAndDecay(interactionCount int, summary string) {
	m.mu.RLock()
	archive := m.archiveMem
	interval := m.cfg.ArchiveSaveInterval
	decay := m.decay
	m.mu.RUnlock()

	if archive != nil && interval > 0 && interactionCount%interval == 0 && summary != "" {
		topics := extractKeyTopics(summary, 3)
		archive.AddSession(summary, interval, topics)
		_ = archive.Save()
	}

	if archive != nil && decay != nil && decay.ShouldSweep(interactionCount) {
		forgotten := archive.Decay(decay)
		if forgotten > 0 {
			_ = archive.Save()
		}
	}
}

// buildSystemPrompt constructs the system prompt section from all three memory tiers,
// allocating the total character budget among them.
func (m *MemoryModule) buildSystemPrompt(ctx context.Context) string {
	m.mu.RLock()
	interactions := make([]interaction, len(m.interactions))
	copy(interactions, m.interactions)
	store := m.store
	archive := m.archiveMem
	summarizer := m.summarizer
	budget := m.budget
	cfg := m.cfg
	m.mu.RUnlock()

	if len(interactions) == 0 && (archive == nil || archive.Len() == 0) {
		return ""
	}

	// --- Build each tier's text ---

	// Tier 1: Working Memory (raw interactions)
	tier1Text := m.buildWorkingMemory(interactions, cfg)

	// Tier 2: Summary Memory (hierarchical summaries)
	tier2Text := ""
	if store != nil && summarizer != nil && cfg.EnableSummarization {
		tier2Text = store.BuildContext(-1)
	}

	// Tier 3: Archive Memory (cross-session)
	tier3Text := ""
	if archive != nil {
		tier3Text = archive.BuildContext(cfg.MaxArchiveSessions, cfg.MaxTotalChars)
	}

	// --- Allocate budget ---
	totalBudget := cfg.MaxTotalChars
	workingBudget, summaryBudget, archiveBudget := totalBudget, 0, 0

	if budget != nil {
		workingBudget, summaryBudget, archiveBudget = budget.Allocate(
			totalBudget,
			len([]rune(tier1Text)),
			len([]rune(tier2Text)),
			len([]rune(tier3Text)),
		)
	}

	// --- Truncate each tier to its budget ---
	tier1Text = truncateByRunes(tier1Text, workingBudget)
	tier2Text = truncateByRunes(tier2Text, summaryBudget)
	tier3Text = truncateByRunes(tier3Text, archiveBudget)

	// --- Assemble in order: oldest (archive) → summary → newest (working) ---
	var sb strings.Builder
	if tier3Text != "" {
		sb.WriteString(tier3Text)
		sb.WriteString("\n\n")
	}
	if tier2Text != "" {
		sb.WriteString(tier2Text)
		sb.WriteString("\n\n")
	}
	if tier1Text != "" {
		sb.WriteString(tier1Text)
	}

	// Final safety: trim to total budget
	result := sb.String()
	runes := []rune(result)
	if len(runes) > totalBudget {
		result = string(runes[:totalBudget]) + "\n[...memory truncated: exceeded total budget]\n"
	}

	return result
}

// buildWorkingMemory formats the raw interaction list (Tier 1) into a string.
// Includes user messages and, when available, assistant responses and summaries (P1).
func (m *MemoryModule) buildWorkingMemory(interactions []interaction, cfg MemoryConfig) string {
	if len(interactions) == 0 {
		return ""
	}

	// Use MemoryStore summaries if summarization is enabled and store exists
	m.mu.RLock()
	store := m.store
	summarizer := m.summarizer
	m.mu.RUnlock()

	if store != nil && summarizer != nil && cfg.EnableSummarization {
		// Keep only interactions not yet covered by MemoryStore
		storeLeafCount := store.Len()
		var uncovered []interaction
		if storeLeafCount < len(interactions) {
			uncovered = interactions[storeLeafCount:]
		}
		if len(uncovered) == 0 {
			return ""
		}
		interactions = uncovered
	}

	// Build string
	var sb strings.Builder
	sb.WriteString("[Recent conversation]\n")
	for i, inter := range interactions {
		if cfg.EnableImportanceScoring && inter.importance < 0.2 {
			continue
		}
		userMsg := truncateString(inter.userMsg, cfg.TruncateMessageLen)
		sb.WriteString(fmt.Sprintf("  [%d] user: %s\n", i+1, userMsg))
		if inter.summary != "" {
			sb.WriteString(fmt.Sprintf("  [%d] assistant: %s\n", i+1, inter.summary))
		}
	}
	return sb.String()
}

// SaveInteraction saves the current interaction. This is the public API equivalent
// of saveInteraction but with locking.
//
// SaveInteraction 保存当前交互；这是 saveInteraction 的公共 API 等价形式，
// 但自带锁。
func (m *MemoryModule) SaveInteraction(ctx context.Context, msgs []*types.Message) error {
	if err := m.saveInteraction(ctx, msgs, ""); err != nil {
		return err
	}

	// P6: also trigger archive and decay
	m.mu.RLock()
	interactionCount := m.interactionCount
	m.mu.RUnlock()

	// Generate summary if available from latest interaction
	m.mu.RLock()
	summary := ""
	if len(m.interactions) > 0 {
		summary = m.interactions[len(m.interactions)-1].summary
	}
	m.mu.RUnlock()

	m.doArchiveAndDecay(interactionCount, summary)
	return nil
}

func (m *MemoryModule) saveInteraction(ctx context.Context, msgs []*types.Message, summary string) error {
	// Extract only the last user message text from the message list (P2).
	userMsg := lastUserMessage(msgs)

	// Calculate importance score if scorer is available
	importance := 0.0
	scorer := m.scorer
	if scorer != nil {
		importance = scorer.Score(ctx, msgs, &ScoreInfo{
			InteractionIndex: len(m.interactions),
			InteractionCount: len(m.interactions) + 1,
			RecencyRatio:     float64(len(m.interactions)) / float64(maxInt(len(m.interactions), 1)),
		})
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.interactions = append(m.interactions, interaction{
		userMsg:    userMsg,
		summary:    summary,
		importance: importance,
	})

	// Evict if over limit
	if len(m.interactions) > m.cfg.MaxInteractions {
		excess := len(m.interactions) - m.cfg.MaxInteractions
		if m.cfg.EnableImportanceScoring && scorer != nil {
			m.evictLowestImportanceLocked(excess)
		} else {
			m.interactions = m.interactions[excess:]
		}
	}

	// Insert into MemoryStore (Tier 2) if enabled
	store := m.store
	if store != nil && m.cfg.EnableSummarization && summary != "" {
		store.Insert(ctx, summary, len(m.interactions)-1)
	}

	return nil
}

// evictLowestImportanceLocked removes the 'count' lowest-scoring interactions.
// Preserves recency order of the remaining interactions (P3).
func (m *MemoryModule) evictLowestImportanceLocked(count int) {
	if count <= 0 || len(m.interactions) <= count {
		m.interactions = nil
		return
	}

	// Tag each interaction with its original index, sort by importance,
	// drop lowest, then restore original order.
	type tagged struct {
		idx  int
		item interaction
	}
	taggedItems := make([]tagged, len(m.interactions))
	for i, inter := range m.interactions {
		taggedItems[i] = tagged{idx: i, item: inter}
	}

	sort.Slice(taggedItems, func(i, j int) bool {
		return taggedItems[i].item.importance < taggedItems[j].item.importance
	})
	taggedItems = taggedItems[count:] // drop lowest-scoring

	sort.Slice(taggedItems, func(i, j int) bool {
		return taggedItems[i].idx < taggedItems[j].idx
	})

	result := make([]interaction, len(taggedItems))
	for i, t := range taggedItems {
		result[i] = t.item
	}
	m.interactions = result
}

// Reset clears all stored interactions and archives the current session if enough data exists.
//
// Reset 清空所有已存交互；数据足够时会把当前会话归档。
func (m *MemoryModule) Reset() {
	m.mu.Lock()

	// Archive current session if there's enough data
	archive := m.archiveMem
	interactionCount := m.interactionCount
	summarizer := m.summarizer

	// Build a session-level summary from the last N interactions
	var sessionSummary string
	if archive != nil && interactionCount > 0 && summarizer != nil {
		start := len(m.interactions) - minInt(3, len(m.interactions))
		var parts []string
		for i := start; i < len(m.interactions); i++ {
			if m.interactions[i].summary != "" {
				parts = append(parts, m.interactions[i].summary)
			}
		}
		if len(parts) > 0 {
			sessionSummary = strings.Join(parts, "; ")
		}
	}

	m.interactions = nil
	m.interactionCount = 0
	store := m.store
	m.mu.Unlock()

	if archive != nil && sessionSummary != "" {
		topics := extractKeyTopics(sessionSummary, 3)
		archive.AddSession(sessionSummary, interactionCount, topics)
		_ = archive.Save() // non-fatal
	}

	if store != nil {
		store.Clear()
	}
}

// Len returns the number of stored interactions.
//
// Len 返回已存交互的数量。
func (m *MemoryModule) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.interactions)
}

// Summarizer returns the configured summarizer, or nil.
//
// Summarizer 返回已配置的摘要器；未配置时返回 nil。
func (m *MemoryModule) Summarizer() Summarizer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.summarizer
}

// Scorer returns the configured scorer, or nil.
//
// Scorer 返回已配置的评分器；未配置时返回 nil。
func (m *MemoryModule) Scorer() Scorer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scorer
}

// Store returns the configured memory store, or nil.
//
// Store 返回已配置的记忆存储；未配置时返回 nil。
func (m *MemoryModule) Store() *MemoryStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store
}

// Archive returns the configured archive memory, or nil.
//
// Archive 返回已配置的归档记忆；未配置时返回 nil。
func (m *MemoryModule) Archive() *ArchiveMemory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.archiveMem
}

// Budget returns the token budget allocator, or nil.
//
// Budget 返回 token 预算分配器；未配置时返回 nil。
func (m *MemoryModule) Budget() *TokenBudget {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.budget
}

// Decay returns the memory decay engine, or nil.
//
// Decay 返回记忆衰减引擎；未配置时返回 nil。
func (m *MemoryModule) Decay() *MemoryDecay {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.decay
}

// lastUserMessage extracts the last user message text from a message list.
func lastUserMessage(msgs []*types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i] != nil && msgs[i].Role == types.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateByRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}
