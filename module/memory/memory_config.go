package memory

// MemoryConfig configures the sliding window memory.
//
// MemoryConfig 配置滑动窗口记忆。
type MemoryConfig struct {
	// MaxInteractions is the maximum number of interaction rounds to remember (default 10).
	MaxInteractions int `json:"max_interactions"`

	// TruncateMessageLen is the maximum characters per message in the system prompt (default 200).
	TruncateMessageLen int `json:"truncate_message_len"`

	// MaxTotalChars is the maximum total characters in the memory section of the system prompt (default 4096).
	MaxTotalChars int `json:"max_total_chars"`

	// EnableSummarization enables LLM-based summarization for compressed interactions.
	// Requires a Summarizer to be set on the module.
	EnableSummarization bool `json:"enable_summarization"`

	// EnableImportanceScoring enables importance-based eviction.
	// When true, low-scoring interactions are evicted first instead of oldest-first.
	EnableImportanceScoring bool `json:"enable_importance_scoring"`

	// StoreGroupSize controls how many leaf summaries are merged into one parent
	// in the hierarchical MemoryStore. Default 5. Only used when EnableSummarization
	// is true and a Summarizer is set.
	StoreGroupSize int `json:"store_group_size"`

	// Budget configures the token budget allocation across working/summary/archive tiers.
	// If zero-valued, defaults are used.
	Budget BudgetConfig `json:"budget"`

	// ArchivePath is the file path for cross-session archive persistence.
	// If empty, archive memory is disabled.
	ArchivePath string `json:"archive_path"`

	// MaxArchiveSessions is the maximum number of past session summaries to retain (default 5).
	MaxArchiveSessions int `json:"max_archive_sessions"`

	// ArchiveSaveInterval controls after how many interactions a snapshot is saved
	// to the archive. Default 10 (saves every 10 interactions).
	ArchiveSaveInterval int `json:"archive_save_interval"`

	// Decay configures the memory decay and forgetting mechanism.
	// When enabled, archived sessions gradually lose importance over time
	// and are automatically forgotten when importance drops below the threshold.
	Decay DecayConfig `json:"decay"`
}

// DefaultMemoryConfig returns the default memory configuration.
//
// DefaultMemoryConfig 返回默认的记忆配置。
func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		MaxInteractions:         10,
		TruncateMessageLen:      200,
		MaxTotalChars:           4096,
		EnableSummarization:     false,
		EnableImportanceScoring: false,
		StoreGroupSize:          5,
		Budget:                  DefaultBudgetConfig(),
		ArchivePath:             "",
		MaxArchiveSessions:      5,
		ArchiveSaveInterval:     10,
	}
}
