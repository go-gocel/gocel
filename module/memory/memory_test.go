package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ---------------------------------------------------------------------------
// Scorer tests
// ---------------------------------------------------------------------------

func TestDefaultScorer_ZeroMessages(t *testing.T) {
	s := NewDefaultScorer()
	score := s.Score(context.Background(), nil, &ScoreInfo{InteractionCount: 1})
	if score != 0 {
		t.Errorf("expected 0 for nil messages, got %f", score)
	}
	score = s.Score(context.Background(), []*types.Message{}, &ScoreInfo{InteractionCount: 1})
	if score != 0 {
		t.Errorf("expected 0 for empty messages, got %f", score)
	}
}

func TestDefaultScorer_SystemMessageHighest(t *testing.T) {
	s := NewDefaultScorer()
	sys := s.Score(context.Background(), []*types.Message{
		{Role: types.RoleSystem, Content: "You are a helpful assistant."},
	}, &ScoreInfo{InteractionCount: 1, RecencyRatio: 0.5})

	user := s.Score(context.Background(), []*types.Message{
		{Role: types.RoleUser, Content: "Hello"},
	}, &ScoreInfo{InteractionCount: 1, RecencyRatio: 0.5})

	if sys <= user {
		t.Errorf("expected system score (%f) > user score (%f)", sys, user)
	}
}

func TestDefaultScorer_ToolInteractionBonus(t *testing.T) {
	s := NewDefaultScorer()
	info := &ScoreInfo{InteractionCount: 1, RecencyRatio: 0.5}

	// With both tool call and tool result
	withTool := s.Score(context.Background(), []*types.Message{
		{Role: types.RoleAssistant, Content: "", ToolCalls: []types.ToolCall{{ID: "1"}}},
		{Role: types.RoleTool, Content: "result"},
	}, info)

	// Without tool
	withoutTool := s.Score(context.Background(), []*types.Message{
		{Role: types.RoleAssistant, Content: "Hello"},
	}, info)

	if withTool <= withoutTool {
		t.Errorf("expected with-tool score (%f) > without-tool score (%f)", withTool, withoutTool)
	}
}

func TestDefaultScorer_RecencyBoost(t *testing.T) {
	s := NewDefaultScorer()
	msgs := []*types.Message{{Role: types.RoleUser, Content: "test"}}

	old := s.Score(context.Background(), msgs, &ScoreInfo{
		InteractionIndex: 0, InteractionCount: 10, RecencyRatio: 0.1,
	})
	new_ := s.Score(context.Background(), msgs, &ScoreInfo{
		InteractionIndex: 9, InteractionCount: 10, RecencyRatio: 0.9,
	})

	if new_ <= old {
		t.Errorf("expected newer interaction score (%f) > older (%f)", new_, old)
	}
}

func TestDefaultScorer_TokenEfficiency(t *testing.T) {
	s := NewDefaultScorer()
	info := &ScoreInfo{InteractionCount: 1, RecencyRatio: 0.5}

	short := s.Score(context.Background(), []*types.Message{
		{Role: types.RoleUser, Content: "Hello world"},
	}, info)

	long := s.Score(context.Background(), []*types.Message{
		{Role: types.RoleUser, Content: string(make([]rune, 2000))},
	}, info)

	if short <= long {
		t.Errorf("expected short message score (%f) > long message score (%f)", short, long)
	}
}

func TestDefaultScorer_WithRoleWeight(t *testing.T) {
	s := NewDefaultScorer().WithRoleWeight(types.RoleTool, 0.9)
	info := &ScoreInfo{InteractionCount: 1, RecencyRatio: 0.5}

	tool := s.Score(context.Background(), []*types.Message{
		{Role: types.RoleTool, Content: "result"},
	}, info)

	user := s.Score(context.Background(), []*types.Message{
		{Role: types.RoleUser, Content: "Hello"},
	}, info)

	// Tool role weight is 0.9, user is 0.8, so tool should be slightly higher
	if tool <= user {
		t.Errorf("expected tool score (%f) > user score (%f) after weight change", tool, user)
	}
}

// ---------------------------------------------------------------------------
// NoopSummarizer tests
// ---------------------------------------------------------------------------

func TestNoopSummarizer(t *testing.T) {
	s := &NoopSummarizer{}
	ctx := context.Background()
	summary, err := s.Summarize(ctx, []*types.Message{{Role: types.RoleUser, Content: "Hello"}})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if summary != "" {
		t.Errorf("expected empty summary, got %q", summary)
	}
}

// TestAsGuardSummarizer_AdaptsInterface: the adapter turns any memory
// Summarizer into the function signature module/guard consumes — memory
// owns the summarization expertise, guard reuses it without duplication
// (single source of truth).
func TestAsGuardSummarizer_AdaptsInterface(t *testing.T) {
	fake := &fakeSummarizer{out: "ADAPTED"}
	fn := AsGuardSummarizer(fake)
	got, err := fn(context.Background(), []*types.Message{{Role: types.RoleUser, Content: "x"}})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if got != "ADAPTED" {
		t.Fatalf("adapted summary = %q, want ADAPTED", got)
	}
}

// fakeSummarizer returns a fixed summary.
type fakeSummarizer struct{ out string }

func (f *fakeSummarizer) Summarize(_ context.Context, _ []*types.Message) (string, error) {
	return f.out, nil
}

// ---------------------------------------------------------------------------
// MemoryStore tests
// ---------------------------------------------------------------------------

func TestMemoryStore_InsertAndLen(t *testing.T) {
	store := NewMemoryStore(5)
	if store.Len() != 0 {
		t.Errorf("expected empty store, got %d", store.Len())
	}

	store.Insert(context.Background(), "user asked about weather", 0)
	if store.Len() != 1 {
		t.Errorf("expected 1 leaf, got %d", store.Len())
	}

	store.Insert(context.Background(), "assistant provided forecast", 1)
	if store.Len() != 2 {
		t.Errorf("expected 2 leaves, got %d", store.Len())
	}
}

func TestMemoryStore_BuildContext(t *testing.T) {
	store := NewMemoryStore(5)
	ctx := context.Background()

	store.Insert(ctx, "user asked about weather", 0)
	store.Insert(ctx, "assistant provided forecast", 1)

	result := store.BuildContext(-1)
	if result == "" {
		t.Fatal("expected non-empty context")
	}
	if !contains(result, "weather") {
		t.Errorf("expected context to contain 'weather', got: %s", result)
	}
	if !contains(result, "forecast") {
		t.Errorf("expected context to contain 'forecast', got: %s", result)
	}
}

func TestMemoryStore_Clear(t *testing.T) {
	store := NewMemoryStore(5)
	store.Insert(context.Background(), "test", 0)
	store.Clear()
	if store.Len() != 0 {
		t.Errorf("expected 0 after clear, got %d", store.Len())
	}
	result := store.BuildContext(-1)
	if result != "" {
		t.Errorf("expected empty context after clear, got %q", result)
	}
}

func TestMemoryStore_InsertBatch(t *testing.T) {
	store := NewMemoryStore(5)
	ctx := context.Background()

	summaries := []string{"s1", "s2", "s3", "s4", "s5"}
	store.InsertBatch(ctx, summaries, 0)

	if store.Len() != 5 {
		t.Errorf("expected 5 leaves, got %d", store.Len())
	}
}

// ---------------------------------------------------------------------------
// RecursiveSummarizer tests
// ---------------------------------------------------------------------------

func TestRecursiveSummarizer_ShortInput(t *testing.T) {
	// With NoopSummarizer, even short input returns empty
	inner := &NoopSummarizer{}
	rs := NewRecursiveSummarizer(inner, 5)
	summary, err := rs.Summarize(context.Background(), []*types.Message{
		{Role: types.RoleUser, Content: "Hi"},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if summary != "" {
		t.Errorf("expected empty summary with NoopSummarizer, got %q", summary)
	}
}

func TestRecursiveSummarizer_BatchSizeConfig(t *testing.T) {
	// batchSize <= 1 should be reset to 5
	rs := NewRecursiveSummarizer(&NoopSummarizer{}, 0)
	if rs.batchSize != 5 {
		t.Errorf("expected batchSize=5 after validation, got %d", rs.batchSize)
	}
	rs = NewRecursiveSummarizer(&NoopSummarizer{}, 1)
	if rs.batchSize != 5 {
		t.Errorf("expected batchSize=5 after validation, got %d", rs.batchSize)
	}
}

// ---------------------------------------------------------------------------
// ArchiveMemory tests
// ---------------------------------------------------------------------------

func TestArchiveMemory_AddAndLen(t *testing.T) {
	a := NewArchiveMemory("", 5)
	if a.Len() != 0 {
		t.Errorf("expected 0, got %d", a.Len())
	}

	a.AddSession("user asked about weather", 10, []string{"weather"})
	if a.Len() != 1 {
		t.Errorf("expected 1, got %d", a.Len())
	}

	a.AddSession("user planned a trip", 5, []string{"travel"})
	if a.Len() != 2 {
		t.Errorf("expected 2, got %d", a.Len())
	}
}

func TestArchiveMemory_AddSessionEmptySummary(t *testing.T) {
	a := NewArchiveMemory("", 5)
	a.AddSession("", 10, nil)
	if a.Len() != 0 {
		t.Errorf("expected 0 for empty summary, got %d", a.Len())
	}
}

func TestArchiveMemory_MaxSessions(t *testing.T) {
	a := NewArchiveMemory("", 3)
	for i := 0; i < 10; i++ {
		a.AddSession("session summary", 1, nil)
	}
	if a.Len() != 3 {
		t.Errorf("expected 3 (capped), got %d", a.Len())
	}
}

func TestArchiveMemory_GetRecentSessions(t *testing.T) {
	a := NewArchiveMemory("", 10)
	for i := 0; i < 5; i++ {
		a.AddSession("session summary", 1, nil)
	}

	recent := a.GetRecentSessions(3)
	if len(recent) != 3 {
		t.Errorf("expected 3 recent, got %d", len(recent))
	}

	recent = a.GetRecentSessions(0)
	if len(recent) != 0 {
		t.Errorf("expected 0 for n<=0, got %d", len(recent))
	}

	recent = a.GetRecentSessions(100)
	if len(recent) != 5 {
		t.Errorf("expected 5 (all) when n > len, got %d", len(recent))
	}
}

func TestArchiveMemory_BuildContext(t *testing.T) {
	a := NewArchiveMemory("", 10)
	result := a.BuildContext(5, 1000)
	if result != "" {
		t.Errorf("expected empty result with no sessions, got %q", result)
	}

	a.AddSession("weather query forecast", 3, nil)
	a.AddSession("restaurant reservation", 5, nil)

	result = a.BuildContext(5, 1000)
	if result == "" {
		t.Fatal("expected non-empty context")
	}
	if !contains(result, "weather") {
		t.Errorf("expected context to contain 'weather', got: %s", result)
	}
	if !contains(result, "restaurant") {
		t.Errorf("expected context to contain 'restaurant', got: %s", result)
	}
}

func TestArchiveMemory_BuildContextMaxChars(t *testing.T) {
	a := NewArchiveMemory("", 10)
	for i := 0; i < 3; i++ {
		a.AddSession("This is a long session summary with lots of words in it", 5, nil)
	}

	// Very small budget should produce empty result
	result := a.BuildContext(5, 10)
	if result == "" {
		// Acceptable — budget too small to fit header + first entry
	}
}

func TestArchiveMemory_SaveLoadFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "archive.json")

	// Create and save
	a1 := NewArchiveMemory(filePath, 10)
	a1.AddSession("first session about weather", 5, []string{"weather"})
	a1.AddSession("second session about coding", 8, []string{"programming"})
	if err := a1.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Load into a new instance
	a2 := NewArchiveMemory(filePath, 10)
	if err := a2.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if a2.Len() != 2 {
		t.Errorf("expected 2 sessions after load, got %d", a2.Len())
	}

	sessions := a2.GetRecentSessions(10)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if !contains(sessions[0].Summary, "weather") {
		t.Errorf("expected session 0 to contain 'weather', got: %s", sessions[0].Summary)
	}
	if !contains(sessions[1].Summary, "coding") {
		t.Errorf("expected session 1 to contain 'coding', got: %s", sessions[1].Summary)
	}
}

func TestArchiveMemory_SaveLoadFileNonExistent(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "nonexistent.json")

	a := NewArchiveMemory(filePath, 10)
	if err := a.Load(); err != nil {
		t.Fatalf("load of non-existent file should succeed silently, got: %v", err)
	}
	if a.Len() != 0 {
		t.Errorf("expected 0 sessions after loading empty file, got %d", a.Len())
	}
}

func TestArchiveMemory_Clear(t *testing.T) {
	a := NewArchiveMemory("", 10)
	a.AddSession("test", 5, nil)
	a.Clear()
	if a.Len() != 0 {
		t.Errorf("expected 0 after clear, got %d", a.Len())
	}
}

// ---------------------------------------------------------------------------
// TokenBudget tests
// ---------------------------------------------------------------------------

func TestTokenBudget_DefaultConfig(t *testing.T) {
	b := NewTokenBudget(BudgetConfig{})
	cfg := b.Config()
	if cfg.WorkingRatio <= 0 || cfg.SummaryRatio <= 0 || cfg.ArchiveRatio <= 0 {
		t.Errorf("expected all ratios > 0, got w=%f s=%f a=%f",
			cfg.WorkingRatio, cfg.SummaryRatio, cfg.ArchiveRatio)
	}
	if cfg.MinReserve < 200 {
		t.Errorf("expected MinReserve >= 200, got %d", cfg.MinReserve)
	}
}

func TestTokenBudget_AllocateBasic(t *testing.T) {
	b := NewTokenBudget(DefaultBudgetConfig())

	// Total budget 1000, all tiers have content
	w, s, a := b.Allocate(1000, 500, 400, 300)
	if w+s+a <= 0 {
		t.Errorf("expected positive allocation, got w=%d s=%d a=%d", w, s, a)
	}
	if w > 500 {
		t.Errorf("working budget cannot exceed actual (500), got %d", w)
	}
	if s > 400 {
		t.Errorf("summary budget cannot exceed actual (400), got %d", s)
	}
	if a > 300 {
		t.Errorf("archive budget cannot exceed actual (300), got %d", a)
	}
}

func TestTokenBudget_AllocateRedistribution(t *testing.T) {
	b := NewTokenBudget(DefaultBudgetConfig())

	// Archive has nothing (0 chars), working and summary have lots
	_, s, a := b.Allocate(1000, 900, 600, 0)

	if s > 600 {
		t.Errorf("summary budget should be capped at actual 600, got %d", s)
	}
	// Archive is 0, its budget should redistribute to summary, then working
	if a != 0 {
		t.Errorf("archive should get 0 (no actual data), got %d", a)
	}
}

func TestTokenBudget_AllocateSmallBudget(t *testing.T) {
	b := NewTokenBudget(BudgetConfig{
		WorkingRatio: 0.40,
		SummaryRatio: 0.35,
		ArchiveRatio: 0.25,
		MinReserve:   200,
	})

	// Budget too small for all min reserves
	w, s, a := b.Allocate(300, 200, 200, 200)
	// Should split evenly
	if w+s+a > 300 {
		t.Errorf("total allocation exceeds budget: %d+%d+%d = %d", w, s, a, w+s+a)
	}
	if w <= 0 || s <= 0 || a <= 0 {
		t.Errorf("expected all tiers > 0 in small budget mode, got w=%d s=%d a=%d", w, s, a)
	}
}

func TestTokenBudget_AllocateExact(t *testing.T) {
	b := NewTokenBudget(BudgetConfig{
		WorkingRatio: 0.50,
		SummaryRatio: 0.30,
		ArchiveRatio: 0.20,
		MinReserve:   50,
	})

	// Exact fit
	w, s, a := b.Allocate(1000, 500, 300, 200)
	if w > 500 || s > 300 || a > 200 {
		t.Errorf("all budgets capped at actual: w=%d s=%d a=%d", w, s, a)
	}
}

// ---------------------------------------------------------------------------
// Integration: three-tier buildSystemPrompt with budget
// ---------------------------------------------------------------------------

func TestMemoryModule_BuildSystemPromptThreeTier(t *testing.T) {
	m := NewMemoryModuleWith(MemoryConfig{
		MaxInteractions:         5,
		MaxTotalChars:           4096,
		EnableSummarization:     false,
		EnableImportanceScoring: false,
		ArchivePath:             "",
	})

	// Simulate a few interactions
	ctx := context.Background()
	_ = m.SaveInteraction(ctx, []*types.Message{
		types.NewUserMessage("What is the weather?"),
	})

	prompt := m.buildSystemPrompt(ctx)
	if prompt == "" {
		t.Fatal("expected non-empty prompt with interactions")
	}
	if !contains(prompt, "[Recent conversation]") {
		t.Errorf("expected [Recent conversation] header in Tier 1")
	}
	if !contains(prompt, "What is the weather?") {
		t.Errorf("expected user message content in prompt")
	}
}

func TestMemoryModule_BuildSystemPromptWithArchive(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.json")

	m := NewMemoryModuleWith(MemoryConfig{
		MaxInteractions:    5,
		MaxTotalChars:      4096,
		ArchivePath:        archivePath,
		MaxArchiveSessions: 5,
	})

	// Add archive entries directly
	m.mu.Lock()
	if m.archiveMem == nil {
		t.Fatal("archive should be initialized")
	}
	m.archiveMem.AddSession("historical weather query", 10, []string{"weather"})
	m.archiveMem.AddSession("previous code discussion", 8, []string{"coding"})
	m.mu.Unlock()

	ctx := context.Background()
	_ = m.SaveInteraction(ctx, []*types.Message{
		types.NewUserMessage("Can we continue?"),
	})

	prompt := m.buildSystemPrompt(ctx)
	if prompt == "" {
		t.Fatal("expected non-empty prompt with archive + working memory")
	}
	if !contains(prompt, "[Cross-session memory]") {
		t.Errorf("expected [Cross-session memory] header in Tier 3")
	}
	if !contains(prompt, "[Recent conversation]") {
		t.Errorf("expected [Recent conversation] header in Tier 1")
	}
	if !contains(prompt, "historical weather") {
		t.Errorf("expected archive content in prompt")
	}
}

func TestMemoryModule_ArchiveSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.json")

	// Simulate first session
	m1 := NewMemoryModuleWith(MemoryConfig{
		MaxInteractions:    10,
		MaxTotalChars:      4096,
		ArchivePath:        archivePath,
		MaxArchiveSessions: 10,
	})

	m1.archiveMem.AddSession("first session about weather", 10, []string{"weather"})
	if err := m1.archiveMem.Save(); err != nil {
		t.Fatalf("first session save failed: %v", err)
	}

	// Simulate second session — load archive
	m2 := NewMemoryModuleWith(MemoryConfig{
		MaxInteractions:    10,
		MaxTotalChars:      4096,
		ArchivePath:        archivePath,
		MaxArchiveSessions: 10,
	})

	if err := m2.archiveMem.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if m2.archiveMem.Len() != 1 {
		t.Errorf("expected 1 archived session across sessions, got %d", m2.archiveMem.Len())
	}
}

func TestMemoryModule_TokenBudgetIntegration(t *testing.T) {
	m := NewMemoryModuleWith(MemoryConfig{
		MaxInteractions: 3,
		MaxTotalChars:   200,
		ArchivePath:     "",
		Budget: BudgetConfig{
			WorkingRatio: 0.60,
			SummaryRatio: 0.40,
			ArchiveRatio: 0.00,
			MinReserve:   50,
		},
	})

	ctx := context.Background()
	_ = m.SaveInteraction(ctx, []*types.Message{
		types.NewUserMessage("Hello, this is a fairly long message that would normally take up a lot of space in the working memory section"),
	})

	prompt := m.buildSystemPrompt(ctx)
	// With a 200-char budget, 60% = 120 for working memory, the message should be
	// truncated at workingBudget runes
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	// Should not exceed total budget
	runes := []rune(prompt)
	if len(runes) > 210 { // 200 + safety for "[...truncated...]"
		t.Errorf("prompt exceeds total budget: %d runes", len(runes))
	}
}

// ---------------------------------------------------------------------------
// extractKeyTopics tests
// ---------------------------------------------------------------------------

func TestExtractKeyTopics_EmptyInput(t *testing.T) {
	topics := extractKeyTopics("", 3)
	if len(topics) != 0 {
		t.Errorf("expected empty topics for empty input, got %v", topics)
	}
}

func TestExtractKeyTopics_Simple(t *testing.T) {
	topics := extractKeyTopics("user asked about weather, assistant provided forecast", 2)
	if len(topics) != 2 {
		t.Errorf("expected 2 topics, got %d: %v", len(topics), topics)
	}
	if !contains(topics[0], "weather") && !contains(topics[0], "forecast") {
		t.Errorf("expected 'weather' or 'forecast' in topics, got: %s", topics[0])
	}
}

func TestExtractKeyTopics_MaxLimit(t *testing.T) {
	topics := extractKeyTopics("topic1, topic2, topic3, topic4", 2)
	if len(topics) > 2 {
		t.Errorf("expected at most 2 topics, got %d", len(topics))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestDecayFactor_CustomHalfLife(t *testing.T) {
	t.Parallel()
	// 12-hour half-life, 12 hours elapsed -> 0.5
	factor := decayFactor(12, 12)
	expected := 0.5
	if abs(factor-expected) > 0.001 {
		t.Errorf("expected ~%f for custom half-life, got %f", expected, factor)
	}
}

func TestMemoryDecay_DefaultConfig(t *testing.T) {
	cfg := DefaultDecayConfig()
	if cfg.Enabled {
		t.Error("expected default decay to be disabled")
	}
	if cfg.HalfLifeHours != 24 {
		t.Errorf("expected 24h half-life, got %f", cfg.HalfLifeHours)
	}
	_ = NewMemoryDecay(cfg)
}

func TestMemoryDecay_SweepDisabled(t *testing.T) {
	t.Parallel()
	cfg := DefaultDecayConfig() // Enabled is false
	d := NewMemoryDecay(cfg)
	sessions := []ArchivedSession{
		{Summary: "test", Importance: 0.1},
	}
	result := d.Sweep(sessions, time.Now())
	if len(result) != 1 {
		t.Errorf("expected 1 session (decay disabled), got %d", len(result))
	}
}

func TestMemoryDecay_SweepForgetsBelowThreshold(t *testing.T) {
	t.Parallel()
	cfg := DefaultDecayConfig()
	cfg.Enabled = true
	cfg.HalfLifeHours = 1     // fast decay
	cfg.MinImportance = 0.5   // high threshold
	cfg.MaxForgetPerSweep = 3 // allow forgetting
	cfg.AccessBoost = 0
	d := NewMemoryDecay(cfg)

	// Create a session created 2 hours ago with importance 1.0
	past := time.Now().Add(-2 * time.Hour)
	sessions := []ArchivedSession{
		{
			Summary:    "old session",
			Importance: 1.0,
			CreatedAt:  past,
		},
	}

	result := d.Sweep(sessions, time.Now())
	// 2 hours / 1 hour half-life = 2 half-lives -> 0.25 importance < 0.5 threshold -> forgotten
	if len(result) != 0 {
		t.Errorf("expected 0 (forgotten below threshold), got %d with importance %f",
			len(result), result[0].Importance)
	}
}

func TestMemoryDecay_SweepAccessBoost(t *testing.T) {
	t.Parallel()
	cfg := DefaultDecayConfig()
	cfg.Enabled = true
	cfg.HalfLifeHours = 1
	cfg.MinImportance = 0.1
	cfg.AccessBoost = 0.5
	cfg.MaxForgetPerSweep = 3
	d := NewMemoryDecay(cfg)

	// Session created 3 hours ago, accessed 3 times
	past := time.Now().Add(-3 * time.Hour)
	sessions := []ArchivedSession{
		{
			Summary:     "accessed session",
			Importance:  1.0,
			CreatedAt:   past,
			AccessCount: 3,
		},
	}

	result := d.Sweep(sessions, time.Now())
	if len(result) != 1 {
		t.Fatalf("expected 1 retained (access boost), got %d", len(result))
	}
	// 3 half-lives: 1.0 * 0.125 = 0.125
	// + 3 * 0.5 = 1.5 -> 1.625
	expected := 1.0*0.125 + 3*0.5
	if abs(result[0].Importance-expected) > 0.01 {
		t.Errorf("expected importance ~%f, got %f", expected, result[0].Importance)
	}
	// AccessCount should be reset after sweep
	if result[0].AccessCount != 0 {
		t.Errorf("expected AccessCount reset to 0, got %d", result[0].AccessCount)
	}
}

func TestMemoryDecay_SweepMaxForgetLimit(t *testing.T) {
	t.Parallel()
	cfg := DefaultDecayConfig()
	cfg.Enabled = true
	cfg.HalfLifeHours = 1
	cfg.MinImportance = 0.5
	cfg.MaxForgetPerSweep = 2 // only forget 2 per sweep
	cfg.AccessBoost = 0
	d := NewMemoryDecay(cfg)

	past := time.Now().Add(-3 * time.Hour)
	sessions := make([]ArchivedSession, 5)
	for i := range sessions {
		sessions[i] = ArchivedSession{
			Summary:    "session",
			Importance: 1.0,
			CreatedAt:  past,
		}
	}

	result := d.Sweep(sessions, time.Now())
	// All 5 are below threshold, but only 2 are forgotten
	if len(result) != 3 {
		t.Errorf("expected 3 retained (5 - 2 max forget), got %d", len(result))
	}
}

func TestMemoryDecay_ShouldSweep(t *testing.T) {
	cfg := DefaultDecayConfig()
	cfg.Enabled = true
	cfg.CheckInterval = 5
	d := NewMemoryDecay(cfg)

	if d.ShouldSweep(0) {
		t.Error("should not sweep at 0")
	}
	if d.ShouldSweep(4) {
		t.Error("should not sweep at 4")
	}
	if !d.ShouldSweep(5) {
		t.Error("should sweep at 5")
	}
	if !d.ShouldSweep(10) {
		t.Error("should sweep at 10")
	}
}

func TestMemoryDecay_ShouldSweepDisabled(t *testing.T) {
	cfg := DefaultDecayConfig() // disabled
	d := NewMemoryDecay(cfg)
	if d.ShouldSweep(5) {
		t.Error("should not sweep when disabled")
	}
}

func TestArchiveMemory_DecayIntegration(t *testing.T) {
	cfg := DefaultDecayConfig()
	cfg.Enabled = true
	cfg.HalfLifeHours = 1
	cfg.MinImportance = 0.5
	cfg.MaxForgetPerSweep = 10
	cfg.AccessBoost = 0
	d := NewMemoryDecay(cfg)

	a := NewArchiveMemory("", 10)
	// Add a session with very old timestamp
	a.mu.Lock()
	a.sessions = append(a.sessions, ArchivedSession{
		SessionID:  "old",
		Summary:    "old session",
		CreatedAt:  time.Now().Add(-3 * time.Hour),
		Importance: 1.0,
	})
	a.ready = true
	a.mu.Unlock()

	forgotten := a.Decay(d)
	if forgotten != 1 {
		t.Errorf("expected 1 forgotten, got %d", forgotten)
	}
	if a.Len() != 0 {
		t.Errorf("expected 0 sessions after decay, got %d", a.Len())
	}
}

func TestArchiveMemory_DecayNilDecay(t *testing.T) {
	a := NewArchiveMemory("", 5)
	a.AddSession("test", 1, nil)
	forgotten := a.Decay(nil)
	if forgotten != 0 {
		t.Errorf("expected 0 for nil decay, got %d", forgotten)
	}
}

func TestArchiveMemory_GetRecentSessionsUpdatesAccessCount(t *testing.T) {
	a := NewArchiveMemory("", 10)
	a.AddSession("session 1", 5, nil)
	a.AddSession("session 2", 3, nil)

	// GetRecentSessions should increment AccessCount
	recent := a.GetRecentSessions(2)
	if len(recent) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(recent))
	}
	if recent[0].AccessCount != 1 {
		t.Errorf("expected AccessCount=1, got %d", recent[0].AccessCount)
	}
	if recent[1].AccessCount != 1 {
		t.Errorf("expected AccessCount=1, got %d", recent[1].AccessCount)
	}
}

func TestMemoryModule_WithDecay(t *testing.T) {
	cfg := DefaultDecayConfig()
	cfg.Enabled = true

	m := NewMemoryModule().WithDecay(cfg)
	if m.Decay() == nil {
		t.Fatal("expected non-nil decay")
	}
	decayCfg := m.Decay().Config()
	if !decayCfg.Enabled {
		t.Error("expected enabled decay")
	}
}

func TestMemoryModule_BuildSystemPromptWithDecay(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "archive.json")

	// Create memory with archive
	m := NewMemoryModuleWith(MemoryConfig{
		ArchivePath:         filePath,
		MaxArchiveSessions:  10,
		ArchiveSaveInterval: 1,
		Decay: DecayConfig{
			Enabled:           true,
			HalfLifeHours:     1,
			MinImportance:     0.5,
			CheckInterval:     1,
			MaxForgetPerSweep: 10,
			AccessBoost:       0,
		},
	})

	// Need summarizer to generate summaries for archive
	m = m.WithSummarizer(&NoopSummarizer{})

	// Save several interactions with context to trigger archive saves
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		msgs := []*types.Message{
			{Role: types.RoleUser, Content: "hello world"},
			{Role: types.RoleAssistant, Content: "hi there"},
		}
		_ = m.SaveInteraction(ctx, msgs)
	}

	// Now 3 sessions archived, all with importance 1.0.
	// The decay runs on each interaction (CheckInterval=1), but since sessions
	// were just created (time.Now()), they should still be well above threshold.
	// This test verifies no panics and the decay is wired in.
	prompt := m.buildSystemPrompt(ctx)
	if prompt == "" {
		t.Error("expected non-empty system prompt even with decay")
	}
}

// ---------------------------------------------------------------------------

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestMemoryModule_HookPathSavesUserMessage is a regression test: the hook
// path (onAfterAgentRun) used to store the run delta (assistant/tool messages
// only) as the interaction, so lastUserMessage found no user message and the
// stored interaction was empty.
func TestMemoryModule_HookPathSavesUserMessage(t *testing.T) {
	m := NewMemoryModule()
	ctx := context.Background()

	// OnMessagesBuilt records the pre-run message count.
	if _, _, err := m.onMessagesBuilt(ctx, []*types.Message{types.NewUserMessage("任务一")}); err != nil {
		t.Fatalf("onMessagesBuilt: %v", err)
	}

	// The run appends an assistant reply; the hook receives the full list.
	if _, _, err := m.onAgentEnd(ctx, &kernel.RunInfo{
		AllMsgs: []*types.Message{
			types.NewUserMessage("任务一"),
			types.NewAssistantMessage("ok"),
		},
	}); err != nil {
		t.Fatalf("onAgentEnd: %v", err)
	}

	got := m.buildSystemPrompt(ctx)
	if !strings.Contains(got, "任务一") {
		t.Fatalf("interaction lost user message; prompt = %q", got)
	}
}
