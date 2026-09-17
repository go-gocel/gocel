// Package guard provides GuardModule for context window management.
//
// GuardModule intercepts messages at HookBeforeStep and HookAfterModelCall
// to trim context when token budget is exceeded and calibrate token estimation.
// It supports multiple compression strategies (Simple / Smart / Summary)
// for truncating tool results. Configure via GuardConfig.
//
// GuardModule 在每步执行前和模型调用后拦截，
// 在超出 Token 预算时裁剪上下文，并校准 Token 估算。
// 支持多种压缩策略。通过 GuardConfig 配置。
package guard

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// CompactorStrategy selects the compaction behavior for context compression.
//
// CompactorStrategy 选择上下文压缩的行为策略。
type CompactorStrategy int

const (
	// CompactSimple truncates by keeping a fixed head and tail portion.
	//
	// CompactSimple 通过保留固定的头部与尾部片段进行截断。
	CompactSimple CompactorStrategy = iota
	// CompactSmart preserves structural boundaries (paragraphs, lines).
	//
	// CompactSmart 保留结构边界（段落、行）。
	CompactSmart
	// CompactSummary heuristically preserves the most meaningful content.
	//
	// CompactSummary 以启发式策略保留最有意义的内容。
	CompactSummary
)

// GuardConfig configures context window management.
//
// Fields set to their zero value are left at the module's default.
// To explicitly disable a feature (e.g. line-based truncation), set the
// controlling field to a negative value (interpreted as "disabled").
//
// GuardConfig 配置上下文窗口管理。字段为零值将使用模块默认值；要显式禁用
// 某个特性（如按行截断），把对应字段设为负值（解释为"禁用"）。
type GuardConfig struct {
	// TokenBudget is the token budget for the context window (default 128K-4K).
	// Set to 0 to use default.
	TokenBudget int

	// CompactThreshold is the ratio (0–1) above which tool results are compacted (default 0.85).
	// Set to 0 to use default.
	CompactThreshold float64

	// MinBudget is the minimum budget to reserve (default 2048).
	// Set to 0 to use default.
	MinBudget int

	// ToolTruncateLength is the character threshold for tool message truncation (default 2000).
	// Set to 0 to use default.
	ToolTruncateLength int

	// Strategy selects the compaction algorithm for tool results (default CompactSimple).
	Strategy CompactorStrategy

	// MaxLines is the max lines before truncation in the strategy (default 50).
	// Set to 0 to use default; set to a negative value to disable line-based truncation.
	MaxLines int

	// KeepHeadRatio is the ratio to keep from the head portion (default 0.5).
	// Set to 0 to use default.
	KeepHeadRatio float64

	// MinPreserve is the minimum units to preserve during strategy compaction (default 10).
	// Set to 0 to use default.
	MinPreserve int

	// OnTrim, when non-nil, is invoked after a trim pass that dropped
	// messages, with the dropped messages and the trim report. Callers can
	// archive the dropped history (e.g. into a memory module).
	//
	// OnTrim 非 nil 时，在发生丢消息的裁剪后回调，携带被丢弃的消息与裁剪报告。
	// 调用方可将被裁内容归档（例如写入记忆模块）。
	OnTrim func(dropped []*types.Message, report *types.TrimReport)

	// Summarizer, when non-nil, upgrades over-budget trimming from lossy
	// truncation to summary compaction (DSH compaction semantics): the head
	// of the conversation is summarized into one system message by the
	// summarizer, the recent tail is preserved verbatim, and the summary
	// message carries the dropped history forward instead of discarding it.
	// The summarizer receives the messages to summarize and returns the
	// summary text; it runs at most once per step.
	//
	// Summarizer 非 nil 时，把超预算裁剪从有损截断升级为摘要压缩（DSH
	// compaction 语义）：对话头部由摘要器汇总为一条系统消息、最近尾部
	// 原样保留，摘要消息把被丢弃的历史带向未来而非丢弃。摘要器接收待
	// 汇总消息并返回摘要文本；每步至多执行一次。
	Summarizer func(ctx context.Context, msgs []*types.Message) (string, error)
}

// defaultGuardConfig returns the default configuration values.
func defaultGuardConfig() GuardConfig {
	return GuardConfig{
		TokenBudget:        128000 - 4096,
		CompactThreshold:   0.85,
		MinBudget:          2048,
		ToolTruncateLength: 2000,
		Strategy:           CompactSimple,
		MaxLines:           50,
		KeepHeadRatio:      0.5,
		MinPreserve:        10,
	}
}

// GuardModule manages the context window via hooks.
//
//	HookBeforeStep:     check budget, trim if needed
//	HookAfterModelCall: calibrate token estimation using actual usage
//
// GuardModule 通过钩子管理上下文窗口：HookBeforeStep 检查预算并在必要时裁剪，
// HookAfterModelCall 用实际用量校准 token 估算。
type GuardModule struct {
	mu         sync.Mutex
	cfg        GuardConfig
	emaTokens  float64
	calibrated bool
}

// NewGuardModule creates a guard module with default config.
//
// NewGuardModule 使用默认配置创建守卫模块。
func NewGuardModule() *GuardModule {
	return &GuardModule{
		cfg:       defaultGuardConfig(),
		emaTokens: 4.0,
	}
}

// NewGuardModuleWith creates a guard module with custom config.
//
// Fields set to 0 in cfg are replaced with defaults. Fields set to a
// negative value are treated as "explicitly disabled" (where applicable).
//
// NewGuardModuleWith 使用自定义配置创建守卫模块：cfg 中为 0 的字段替换为默认值，
// 负值字段视为"显式禁用"（在适用处）。
func NewGuardModuleWith(cfg GuardConfig) *GuardModule {
	def := defaultGuardConfig()

	// Validate ranges
	if cfg.CompactThreshold < 0 || cfg.CompactThreshold > 1 {
		cfg.CompactThreshold = 0
	}
	if cfg.Strategy < CompactSimple || cfg.Strategy > CompactSummary {
		cfg.Strategy = CompactSimple
	}

	// Build final config: explicit zero → default, negative → keep as-is (disabled)
	resolve := func(dst *int, src, dflt int) {
		if src > 0 {
			*dst = src
		} else if src == 0 {
			*dst = dflt
		}
		// negative → keep src (explicitly disabled)
	}
	resolveF := func(dst *float64, src, dflt float64) {
		if src > 0 {
			*dst = src
		} else if src == 0 {
			*dst = dflt
		}
	}

	m := NewGuardModule()
	m.cfg.Strategy = cfg.Strategy
	m.cfg.OnTrim = cfg.OnTrim
	m.cfg.Summarizer = cfg.Summarizer

	resolve(&m.cfg.TokenBudget, cfg.TokenBudget, def.TokenBudget)
	resolveF(&m.cfg.CompactThreshold, cfg.CompactThreshold, def.CompactThreshold)
	resolve(&m.cfg.MinBudget, cfg.MinBudget, def.MinBudget)
	resolve(&m.cfg.ToolTruncateLength, cfg.ToolTruncateLength, def.ToolTruncateLength)
	resolve(&m.cfg.MaxLines, cfg.MaxLines, def.MaxLines)
	resolveF(&m.cfg.KeepHeadRatio, cfg.KeepHeadRatio, def.KeepHeadRatio)
	resolve(&m.cfg.MinPreserve, cfg.MinPreserve, def.MinPreserve)

	return m
}

// Register registers the guard hooks on the runtime.
//
// Register 在运行时上注册守卫钩子。
func (m *GuardModule) Register(rt kernel.HookRegistrar) {
	rt.OnStepStart(m.onStepStart)
	rt.OnModelResult(m.onModelResult)
}

func (m *GuardModule) onStepStart(ctx context.Context, stepInfo *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
	msgs := stepInfo.Messages
	if len(msgs) < 1 {
		return ctx, stepInfo, nil
	}

	budget := m.cfg.TokenBudget
	if budget <= 0 {
		return ctx, stepInfo, nil
	}

	// Estimate tokens for ALL messages including system prompt.
	est := m.EstimateTokens(msgs)
	threshold := int(float64(budget) * m.cfg.CompactThreshold)

	if est > budget {
		// Over budget: full trim + compact. When a summarizer is
		// configured, upgrade from lossy truncation to summary compaction:
		// summarize the head, keep the recent tail verbatim, and carry the
		// dropped history forward in the summary message (DSH compaction).
		if m.cfg.Summarizer != nil {
			trimmed, report := m.summaryTrim(ctx, msgs, budget)
			stepInfo.Messages = types.CloneMessages(trimmed)
			if m.cfg.OnTrim != nil {
				m.cfg.OnTrim(droppedMessages(msgs, trimmed), report)
			}
		} else {
			trimmed, report := m.Trim(msgs, budget)
			trimmed = m.compactMessages(trimmed)
			stepInfo.Messages = types.CloneMessages(trimmed)
			if m.cfg.OnTrim != nil {
				m.cfg.OnTrim(droppedMessages(msgs, trimmed), report)
			}
		}
	} else if est > threshold {
		// Near budget: only compress tool messages
		for i, msg := range msgs {
			if msg != nil && msg.Role == types.RoleTool && len(msg.Content) > m.cfg.ToolTruncateLength {
				compacted := m.compactContent(msg.Content)
				if compacted != msg.Content {
					clone := types.CloneMessage(msg)
					clone.Content = compacted
					msgs[i] = clone
				}
			}
		}
	}

	return ctx, stepInfo, nil
}

// droppedMessages returns the messages whose exact content no longer exists
// in the post-trim history, computed as a multiset difference on content
// identity: compaction clones messages (pointer comparison misreported
// EVERY message as dropped — C9), and window trims may remove duplicates
// (a plain set comparison would under-report them). The system message is
// never removed by Trim, so it never appears here.
func droppedMessages(before, after []*types.Message) []*types.Message {
	afterCount := make(map[string]int, len(after))
	for _, m := range after {
		if m != nil {
			afterCount[messageKey(m)]++
		}
	}
	var dropped []*types.Message
	for _, m := range before {
		if m == nil {
			continue
		}
		k := messageKey(m)
		if afterCount[k] > 0 {
			afterCount[k]--
			continue
		}
		dropped = append(dropped, m)
	}
	return dropped
}

// messageKey fingerprints a message for content-identity comparison.
func messageKey(m *types.Message) string {
	return string(m.Role) + "|" + m.ToolCallID + "|" + m.ToolName + "|" + m.Content
}

func (m *GuardModule) onModelResult(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	if info.Usage != nil && info.Usage.PromptTokens > 0 && len(info.Messages) > 0 {
		m.Calibrate(info.Usage.PromptTokens, info.Messages)
	}
	return ctx, info, nil
}

// ShouldTrim checks if messages exceed the budget.
//
// ShouldTrim 检查消息是否超出预算。
func (m *GuardModule) ShouldTrim(msgs []*types.Message, budget int) bool {
	if len(msgs) < 2 || budget <= 0 {
		return false
	}
	return m.EstimateTokens(msgs) > budget
}

// Trim performs progressive context trimming.
//
// Trim 执行渐进式上下文裁剪。
func (m *GuardModule) Trim(msgs []*types.Message, budget int) ([]*types.Message, *types.TrimReport) {
	if budget <= 0 {
		budget = m.cfg.MinBudget
	}
	report := &types.TrimReport{OriginalSize: len(msgs)}

	est := m.EstimateTokens(msgs)
	if est <= budget {
		report.FinalSize = len(msgs)
		return msgs, report
	}

	trimmed := m.compactMessages(msgs)
	est = m.EstimateTokens(trimmed)
	if est <= budget {
		report.Truncated = true
		report.FinalSize = len(trimmed)
		report.Strategy = "compact"
		return trimmed, report
	}

	keepRatio := float64(budget) / float64(est)
	keepCount := int(float64(len(trimmed)) * keepRatio)
	if keepCount < 2 {
		keepCount = 2
	}
	if keepCount > len(trimmed) {
		keepCount = len(trimmed)
	}

	systemMsg := trimmed[0]

	// Window with budget re-check: the ratio estimate can overshoot a
	// token-heavy tail (tool results, reasoning), so shrink the window
	// until the estimate fits — the old code windowed once and never
	// re-checked, leaving the context up to 2-3x over budget (verified
	// defect). The system prompt is never dropped; at the 2-message floor
	// the estimate is accepted as-is.
	for {
		recentMsgs := trimmed[len(trimmed)-keepCount+1:]

		result := make([]*types.Message, 0, 1+len(recentMsgs))
		result = append(result, systemMsg)
		result = append(result, recentMsgs...)

		if m.EstimateTokens(result) <= budget || keepCount <= 2 {
			// 窗口切点可能落在 tool_calls 配对中间：LLM 提供商要求 assistant 的
			// 每条 tool_call 都有对应的 tool 消息接续（否则 400 insufficient tool
			// messages），修复裁剪造成的断链——不完整的历史宁可少给，不能给
			// 非法序列。
			result = repairToolPairing(result)

			report.Truncated = true
			report.FinalSize = len(result)
			report.Strategy = "window"
			return result, report
		}
		keepCount = (keepCount + 1) / 2
	}
}

// summaryTrim performs summary compaction (DSH compaction semantics) when
// the conversation exceeds the budget: the summarizer condenses the head
// into one system message, the recent tail stays verbatim, and the summary
// carries the dropped history forward. The system prompt (message 0) is
// never summarized. A summarizer failure degrades to the ordinary lossy
// Trim — the run must never fail because summarization failed.
//
// summaryTrim 在对话超预算时执行摘要压缩（DSH compaction 语义）：摘要器
// 把头部浓缩为一条系统消息、最近尾部原样保留，摘要把被丢弃的历史带向
// 未来。系统提示词（消息 0）绝不参与摘要。摘要器失败降级为普通有损
// Trim——运行绝不因摘要失败而失败。
func (m *GuardModule) summaryTrim(ctx context.Context, msgs []*types.Message, budget int) ([]*types.Message, *types.TrimReport) {
	report := &types.TrimReport{OriginalSize: len(msgs)}
	if len(msgs) < 3 || budget <= 0 {
		return msgs, report
	}
	// How much head to summarize: keep the tail that fits the budget with
	// room for the summary message itself. The system prompt is always
	// kept; the head is everything after it up to the keep-point.
	system := msgs[0]
	tail := msgs[1:]
	// Reserve the summary message's estimated tokens (a short system line).
	summaryBudget := int(float64(budget) * 0.15)
	if summaryBudget < 64 {
		summaryBudget = 64
	}
	tailBudget := budget - summaryBudget
	if tailBudget < 1 {
		tailBudget = 1
	}
	keepCount := 0
	for i := len(tail) - 1; i >= 0; i-- {
		if m.EstimateTokens(tail[i : i+1]) > tailBudget {
			break
		}
		tailBudget -= m.EstimateTokens(tail[i : i+1])
		keepCount++
	}
	head := tail[:len(tail)-keepCount]
	if len(head) == 0 {
		// Nothing to summarize: fall back to the lossy trim.
		return m.Trim(msgs, budget)
	}
	summary, err := m.cfg.Summarizer(ctx, head)
	if err != nil || strings.TrimSpace(summary) == "" {
		// Summarization failed: degrade to the ordinary lossy trim.
		return m.Trim(msgs, budget)
	}
	// 摘要合并进第 0 条 system（单 system 不变量）。先剥离上一次压缩留下的
	// 旧摘要段再追加新摘要——「替换」而非「累积」，否则每步超预算都会在
	// system 里多留一段过期摘要（system 永不参与 head 摘要，会无限膨胀）。
	system = types.CloneMessage(system)
	system.Content = stripCompactedSummary(system.Content) + "\n\n" + "<compacted-summary>\n" + summary + "\n</compacted-summary>"
	result := append([]*types.Message{system}, tail[len(tail)-keepCount:]...)
	result = repairToolPairing(result)
	report.Truncated = true
	report.FinalSize = len(result)
	report.Strategy = "summary"
	return result, report
}

// summaryOpen / summaryClose 标记压缩摘要段的边界。摘要每次「替换」而非
// 「累积」：summaryTrim 合并前先剥离旧段，避免 system 无限膨胀。
const (
	summaryOpen  = "<compacted-summary>"
	summaryClose = "</compacted-summary>"
)

// stripCompactedSummary removes any previously merged compacted-summary
// block(s) from a system prompt's content, returning the base prompt. The
// summary markup is guard-private, so stripping it here keeps the format's
// single source in this package.
func stripCompactedSummary(content string) string {
	for {
		start := strings.Index(content, summaryOpen)
		if start < 0 {
			return content
		}
		end := strings.Index(content, summaryClose)
		if end < 0 {
			return content
		}
		end += len(summaryClose)
		content = strings.TrimSpace(content[:start] + content[end:])
	}
}

// repairToolPairing 修复窗口裁剪造成的 tool_calls 配对断裂：
//   - 孤立 tool 消息（其 assistant tool_calls 前导已被裁掉）直接丢弃；
//   - 配对不完整的 assistant 组（部分 tool 响应被裁掉）整体丢弃。
func repairToolPairing(msgs []*types.Message) []*types.Message {
	kept := make([]*types.Message, 0, len(msgs))
	i := 0
	for i < len(msgs) {
		m := msgs[i]
		if m == nil {
			i++
			continue
		}
		if m.Role == types.RoleTool {
			// 无前导 assistant 的孤立 tool 消息。
			i++
			continue
		}
		if m.Role != types.RoleAssistant || len(m.ToolCalls) == 0 {
			kept = append(kept, m)
			i++
			continue
		}
		// assistant 带 tool_calls：收集其后紧邻的 tool 消息并校验完整性。
		j := i + 1
		responded := make(map[string]bool, len(m.ToolCalls))
		for j < len(msgs) && msgs[j] != nil && msgs[j].Role == types.RoleTool {
			responded[msgs[j].ToolCallID] = true
			j++
		}
		complete := true
		for _, tc := range m.ToolCalls {
			if tc.ID == "" || !responded[tc.ID] {
				complete = false
				break
			}
		}
		if complete {
			kept = append(kept, msgs[i:j]...)
		}
		// 不完整：整组（assistant + 部分 tool 消息）丢弃。
		i = j
	}
	return kept
}

// Calibrate adjusts the token estimation using actual API token counts.
//
// Calibrate 使用实际的 API token 计数调整 token 估算。
func (m *GuardModule) Calibrate(actualPromptTokens int, messages []*types.Message) {
	if actualPromptTokens <= 0 || len(messages) == 0 {
		return
	}
	est := types.EstimateTokens(messages)
	if est <= 0 {
		return
	}
	ratio := float64(actualPromptTokens) / float64(est)

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.calibrated {
		m.emaTokens = ratio
		m.calibrated = true
	} else {
		alpha := 0.3
		m.emaTokens = alpha*ratio + (1-alpha)*m.emaTokens
	}
}

// EstimateTokens estimates token count with EMA calibration.
//
// EstimateTokens 使用 EMA 校准估算 token 数量。
func (m *GuardModule) EstimateTokens(msgs []*types.Message) int {
	est := types.EstimateTokens(msgs)
	if est == 0 {
		return 0
	}
	m.mu.Lock()
	calibrated := m.calibrated
	ema := m.emaTokens
	m.mu.Unlock()
	if calibrated && ema > 0 {
		return int(float64(est) * ema)
	}
	return est
}

// Calibrated returns whether token estimation has been calibrated.
//
// Calibrated 返回 token 估算是否已完成校准。
func (m *GuardModule) Calibrated() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calibrated
}

// CalibrationFactor returns the current EMA calibration factor, or 1.0 if not calibrated.
//
// CalibrationFactor 返回当前 EMA 校准因子；未校准时返回 1.0。
func (m *GuardModule) CalibrationFactor() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.calibrated {
		return 1.0
	}
	return m.emaTokens
}

// TokenBudget returns the configured token budget.
//
// TokenBudget 返回已配置的 token 预算。
func (m *GuardModule) TokenBudget() int {
	return m.cfg.TokenBudget
}

// CompactContent runs the configured strategy on a single string. Exported for direct use.
// Uses ToolTruncateLength as the character truncation budget.
//
// CompactContent 对单个字符串执行已配置的压缩策略（导出供直接使用），
// 以 ToolTruncateLength 作为字符截断预算。
func (m *GuardModule) CompactContent(content string) string {
	result, _ := m.compactContentWithCfg(content, m.cfg.ToolTruncateLength, m.cfg.MaxLines, m.cfg.KeepHeadRatio, m.cfg.MinPreserve, m.cfg.Strategy)
	return result
}

// compactMessages applies strategy-based compression to all tool messages in the list.
func (m *GuardModule) compactMessages(msgs []*types.Message) []*types.Message {
	result := make([]*types.Message, len(msgs))
	for i, msg := range msgs {
		if msg == nil {
			continue
		}
		cm := types.CloneMessage(msg)
		if cm.Role == types.RoleTool && len([]rune(cm.Content)) > m.cfg.ToolTruncateLength {
			cm.Content = m.compactContent(cm.Content)
		}
		result[i] = cm
	}
	return result
}

// compactContent compresses a tool result string using the configured strategy.
func (m *GuardModule) compactContent(content string) string {
	toolLen := m.cfg.ToolTruncateLength
	if toolLen <= 0 {
		return content
	}
	runes := []rune(content)
	if len(runes) <= toolLen {
		return content
	}

	result, _ := m.compactContentWithCfg(content, toolLen, m.cfg.MaxLines, m.cfg.KeepHeadRatio, m.cfg.MinPreserve, m.cfg.Strategy)
	return result
}

// compactContentWithCfg applies a specific strategy to a content string.
func (m *GuardModule) compactContentWithCfg(content string, maxChars, maxLines int, keepHeadRatio float64, minPreserve int, strategy CompactorStrategy) (string, bool) {
	// Guard: content at or below the threshold must pass through untouched.
	// Without this, strategies that rebuild content (e.g. CompactSummary
	// appending line breaks) would mutate content that needs no compaction.
	if maxChars > 0 && len([]rune(content)) <= maxChars {
		return content, false
	}
	switch strategy {
	case CompactSmart:
		return m.compactSmart(content, maxChars, maxLines, keepHeadRatio, minPreserve)
	case CompactSummary:
		return m.compactSummary(content, maxChars, maxLines, keepHeadRatio, minPreserve)
	default:
		return m.compactSimple(content, maxChars, maxLines, keepHeadRatio, minPreserve)
	}
}

// compactSimple truncates by keeping a fixed head and tail portion.
func (m *GuardModule) compactSimple(content string, maxChars, maxLines int, keepHeadRatio float64, minPreserve int) (string, bool) {
	modified := false

	if maxLines > 0 {
		lines := strings.Split(content, "\n")
		if len(lines) > maxLines {
			keepLines := maxInt(int(float64(maxLines)*keepHeadRatio), minInt(minPreserve, maxLines/2))
			tailLines := maxLines - keepLines
			if tailLines < 1 {
				tailLines = 1
			}
			var sb strings.Builder
			for i := 0; i < keepLines && i < len(lines); i++ {
				sb.WriteString(lines[i])
				sb.WriteByte('\n')
			}
			sb.WriteString("... [truncated " + strconv.Itoa(len(lines)-maxLines) + " lines] ...\n")
			for i := len(lines) - tailLines; i < len(lines); i++ {
				sb.WriteString(lines[i])
				if i < len(lines)-1 {
					sb.WriteByte('\n')
				}
			}
			content = sb.String()
			modified = true
		}
	}

	if maxChars > 0 && utf8.RuneCountInString(content) > maxChars {
		runes := []rune(content)
		headLen := maxInt(int(float64(maxChars)*keepHeadRatio), minInt(minPreserve*20, maxChars/2))
		tailLen := maxChars - headLen
		if tailLen < minInt(minPreserve*20, 200) {
			tailLen = minInt(minPreserve*20, 200)
		}
		if tailLen > maxChars-headLen {
			tailLen = maxChars - headLen
		}
		if tailLen < 1 {
			tailLen = 1
		}
		if headLen+tailLen > len(runes) {
			tailLen = len(runes) - headLen
		}
		truncated := len(runes) - (headLen + tailLen)
		var sb strings.Builder
		sb.WriteString(string(runes[:headLen]))
		sb.WriteString("\n... [truncated " + strconv.Itoa(truncated) + " chars] ...\n")
		sb.WriteString(string(runes[len(runes)-tailLen:]))
		content = sb.String()
		modified = true
	}

	return content, modified
}

// compactSmart preserves structural boundaries (paragraphs, lines).
func (m *GuardModule) compactSmart(content string, maxChars, maxLines int, keepHeadRatio float64, minPreserve int) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return m.compactSimple(content, maxChars, maxLines, keepHeadRatio, minPreserve)
	}

	preserveHead := maxInt(int(float64(maxLines)*keepHeadRatio), minPreserve)
	preserveTail := maxLines - preserveHead
	if preserveTail < minPreserve/2 {
		preserveTail = minPreserve / 2
	}

	var sb strings.Builder
	for i := 0; i < preserveHead && i < len(lines); i++ {
		sb.WriteString(lines[i])
		sb.WriteByte('\n')
	}
	skipped := len(lines) - preserveHead - preserveTail
	if skipped < 0 {
		skipped = 0
	}
	sb.WriteString("... [truncated " + strconv.Itoa(skipped) + " lines] ...\n")
	for i := len(lines) - preserveTail; i < len(lines); i++ {
		sb.WriteString(lines[i])
		if i < len(lines)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String(), true
}

// compactSummary heuristically preserves the most meaningful content.
func (m *GuardModule) compactSummary(content string, maxChars, maxLines int, keepHeadRatio float64, minPreserve int) (string, bool) {
	lines := strings.Split(content, "\n")

	keepLines := maxInt(minPreserve, maxLines/3)
	if keepLines > len(lines) {
		keepLines = len(lines)
	}

	summaryEnd := 0
	foundKeyword := false
	summaryKeywords := []string{"summary:", "result:", "output:", "conclusion:"}

	searchLimit := keepLines / 2
	if searchLimit > len(lines) {
		searchLimit = len(lines)
	}
	for i, line := range lines {
		if i >= searchLimit {
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		summaryEnd = i + 1

		lower := strings.ToLower(trimmed)
		for _, kw := range summaryKeywords {
			if strings.Contains(lower, kw) {
				foundKeyword = true
				summaryEnd = len(lines)
				break
			}
		}
		if foundKeyword {
			break
		}
	}

	switch {
	case foundKeyword:
	case summaryEnd == 0:
		summaryEnd = maxInt(keepLines, 1)
		if summaryEnd > len(lines) {
			summaryEnd = len(lines)
		}
	default:
		summaryEnd = maxInt(summaryEnd, keepLines)
		if summaryEnd > len(lines) {
			summaryEnd = len(lines)
		}
	}

	var sb strings.Builder
	for i := 0; i < summaryEnd && i < len(lines); i++ {
		sb.WriteString(lines[i])
		sb.WriteByte('\n')
	}
	if summaryEnd < len(lines) {
		sb.WriteString("... [summarized " + strconv.Itoa(len(lines)-summaryEnd) + " lines] ...\n")
	}
	return sb.String(), true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
