package memory

// BudgetConfig defines the token budget allocation ratios for the three memory tiers.
// The three ratios should sum to approximately 1.0 (they are normalized on use).
//
// BudgetConfig 定义三个记忆层级间的 token 预算分配比例；三个比例之和应约为
// 1.0（使用时会被归一化）。
type BudgetConfig struct {
	// WorkingRatio is the fraction of budget allocated to working memory (raw interactions).
	// Default 0.40.
	WorkingRatio float64 `json:"working_ratio"`

	// SummaryRatio is the fraction allocated to summary memory (MemoryStore summaries).
	// Default 0.35.
	SummaryRatio float64 `json:"summary_ratio"`

	// ArchiveRatio is the fraction allocated to archive memory (cross-session summaries).
	// Default 0.25.
	ArchiveRatio float64 `json:"archive_ratio"`

	// MinReserve is the minimum character budget guaranteed to each tier (default 200).
	MinReserve int `json:"min_reserve"`
}

// DefaultBudgetConfig returns the default budget allocation (40/35/25).
//
// DefaultBudgetConfig 返回默认预算分配（40/35/25）。
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		WorkingRatio: 0.40,
		SummaryRatio: 0.35,
		ArchiveRatio: 0.25,
		MinReserve:   200,
	}
}

// TokenBudget allocates a total character budget across the three memory tiers.
//
// TokenBudget 将总字符预算分配到三个记忆层级。
type TokenBudget struct {
	cfg BudgetConfig
}

// NewTokenBudget creates a TokenBudget with the given config.
// If ratios sum to zero, defaults are used.
//
// NewTokenBudget 使用给定配置创建 TokenBudget；若比例之和为零则使用默认值。
func NewTokenBudget(cfg BudgetConfig) *TokenBudget {
	// Validate / default ratios
	total := cfg.WorkingRatio + cfg.SummaryRatio + cfg.ArchiveRatio
	if total <= 0 {
		cfg = DefaultBudgetConfig()
	}
	if cfg.MinReserve <= 0 {
		cfg.MinReserve = 200
	}
	return &TokenBudget{cfg: cfg}
}

// Allocate distributes totalBudget among the three tiers based on configured ratios.
//
// actualWorking, actualSummary, actualArchive are the actual string lengths each tier
// would like to use. The allocator caps each tier to its budget share, then redistributes
// any unused budget to the next tier in order: archive → summary → working.
//
// Returns the allocated character budgets (workingChars, summaryChars, archiveChars).
//
// Allocate 按配置的比例把 totalBudget 分配给三个层级。actualWorking、
// actualSummary、actualArchive 是各层级实际想使用的字符串长度：分配器先把每层
// 限制在其预算份额内，再把未用预算按顺序再分配：archive → summary → working。
//
// 返回分配后的字符预算（workingChars, summaryChars, archiveChars）。
func (b *TokenBudget) Allocate(totalBudget, actualWorking, actualSummary, actualArchive int) (int, int, int) {
	if totalBudget <= 0 {
		return 0, 0, 0
	}

	// Normalize ratios to sum to 1.0
	total := b.cfg.WorkingRatio + b.cfg.SummaryRatio + b.cfg.ArchiveRatio

	// Raw allocation per tier
	rawWorking := int(float64(totalBudget) * b.cfg.WorkingRatio / total)
	rawSummary := int(float64(totalBudget) * b.cfg.SummaryRatio / total)
	rawArchive := int(float64(totalBudget) * b.cfg.ArchiveRatio / total)

	// Ensure minimum reserve
	minReserve := b.cfg.MinReserve
	if minReserve*3 > totalBudget {
		// Budget too small to guarantee all min reserves; split evenly
		each := totalBudget / 3
		return minInt(each, actualWorking), minInt(each, actualSummary), minInt(each, actualArchive)
	}

	// Cap each tier to its actual need
	workingBudget := minInt(rawWorking, actualWorking)
	summaryBudget := minInt(rawSummary, actualSummary)
	archiveBudget := minInt(rawArchive, actualArchive)

	// Redistribute unused budget: archive unused → summary; summary unused → working
	archiveUnused := rawArchive - archiveBudget
	if archiveUnused > 0 {
		summaryBudget = minInt(summaryBudget+archiveUnused, actualSummary)
	}

	// Summary unused (after archive redistribution)
	totalSummaryBudgetRaw := rawSummary + archiveUnused
	summaryRemaining := totalSummaryBudgetRaw - summaryBudget
	if summaryRemaining > 0 {
		workingBudget = minInt(workingBudget+summaryRemaining, actualWorking)
	}

	// Final cap: ensure none exceeds totalBudget
	totalUsed := workingBudget + summaryBudget + archiveBudget
	if totalUsed > totalBudget {
		// Scale proportionally
		ratio := float64(totalBudget) / float64(totalUsed)
		workingBudget = int(float64(workingBudget) * ratio)
		summaryBudget = int(float64(summaryBudget) * ratio)
		archiveBudget = int(float64(archiveBudget) * ratio)
	}

	return workingBudget, summaryBudget, archiveBudget
}

// Config returns a copy of the budget configuration.
//
// Config 返回预算配置的副本。
func (b *TokenBudget) Config() BudgetConfig {
	return b.cfg
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
