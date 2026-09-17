package contextfilters

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/spill"
	"github.com/go-gocel/gocel/core/types"
)

// ── mocks ───────────────────────────────────────────────────────────────

type mockRegistrar struct {
	toolResult func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error)
}

func (r *mockRegistrar) OnAgentStart(fn kernel.AgentStartHook) func()       { return nil }
func (r *mockRegistrar) OnAgentEnd(fn kernel.AgentEndHook) func()           { return nil }
func (r *mockRegistrar) OnMessagesBuilt(fn kernel.MessagesHook) func()      { return nil }
func (r *mockRegistrar) OnStepStart(fn kernel.StepHook) func()              { return nil }
func (r *mockRegistrar) OnStepEnd(fn kernel.StepHook) func()                { return nil }
func (r *mockRegistrar) OnModelCall(fn kernel.ModelCallHook) func()         { return nil }
func (r *mockRegistrar) OnModelResult(fn kernel.ModelResultHookFunc) func() { return nil }
func (r *mockRegistrar) OnToolCall(fn kernel.ToolCallHook) func()           { return nil }
func (r *mockRegistrar) OnToolResult(fn kernel.ToolResultHookFunc) func() {
	r.toolResult = fn
	return func() {}
}
func (r *mockRegistrar) OnDecision(fn kernel.DecisionHook) func() { return nil }

type fakeCtx struct {
	sessionID string
}

func (a *fakeCtx) InvocationID() string                    { return "test" }
func (a *fakeCtx) AgentName() string                       { return "" }
func (a *fakeCtx) Branch() string                          { return "" }
func (a *fakeCtx) RunPath() string                         { return "" }
func (a *fakeCtx) ContextPassing() string                  { return "" }
func (a *fakeCtx) EnableStreaming() bool                   { return false }
func (a *fakeCtx) InterruptInput() chan string             { return nil }
func (a *fakeCtx) SendEvent() func(*types.Event) bool      { return func(*types.Event) bool { return true } }
func (a *fakeCtx) ParentAgent() kernel.Agent               { return nil }
func (a *fakeCtx) State() kernel.StateManager              { return nil }
func (a *fakeCtx) Facts() *types.RuntimeFacts              { return &types.RuntimeFacts{SessionID: a.sessionID} }
func (a *fakeCtx) SetSendEvent(fn func(*types.Event) bool) {}
func (a *fakeCtx) SetInterruptInput(ch chan string)        {}

type errStore struct{ err error }

func (s *errStore) SaveText(_ context.Context, owner, text string) (spill.Ref, error) {
	return spill.Ref{}, s.err
}
func (s *errStore) Retrieve(_ context.Context, owner, locator string) (string, error) {
	return "", spill.ErrNotFound
}

func newCtx(sessionID string) context.Context {
	return kernel.WithAgentContext(context.Background(), &fakeCtx{sessionID: sessionID})
}

func hookOf(m *Module) func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	r := &mockRegistrar{}
	m.Register(r)
	return r.toolResult
}

func runResult(hook func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error), ctx context.Context, name, result string) (string, error) {
	_, info, err := hook(ctx, &kernel.ToolCallInfo{Name: name, Result: result})
	if info == nil {
		return "", err
	}
	return info.Result, err
}

// ── pruner ──────────────────────────────────────────────────────────────

func TestPrune_OverThreshold_KeepsHeadTailAndConverges(t *testing.T) {
	m := MustNew(DefaultConfig())
	hook := hookOf(m)
	in := strings.Repeat("abcdefghij", 1000) // 10000 runes
	out, err := runResult(hook, newCtx(""), "tool", in)
	if err != nil {
		t.Fatalf("hook error: %v", err)
	}
	if got := utf8.RuneCountInString(out); got > m.cfg.PruneThresholdChars {
		t.Fatalf("pruned result %d runes > threshold %d", got, m.cfg.PruneThresholdChars)
	}
	if !strings.HasPrefix(out, in[:m.cfg.PruneHeadChars]) {
		t.Fatal("pruned result must keep the head")
	}
	if !strings.Contains(out, PruneMarker) {
		t.Fatal("pruned result must contain the marker")
	}
	if !strings.HasSuffix(out, strings.Repeat("abcdefghij", m.cfg.PruneTailChars/10)) {
		t.Fatal("pruned result must keep the tail")
	}
	// Idempotence: a second pass must not change the result.
	out2, _ := runResult(hook, newCtx(""), "tool", out)
	if out2 != out {
		t.Fatal("pruning must converge: second pass changed the result")
	}
}

func TestPrune_CountsRunesNotBytes(t *testing.T) {
	m := MustNew(Config{PruneThresholdChars: 100, PruneHeadChars: 40, PruneTailChars: 20})
	hook := hookOf(m)
	in := strings.Repeat("中", 200) // 200 runes, 600 bytes
	out, _ := runResult(hook, newCtx(""), "tool", in)
	if got := utf8.RuneCountInString(out); got > 100 {
		t.Fatalf("runes after prune = %d, want ≤ 100", got)
	}
	head := cutRunes(in, 40)
	if !strings.HasPrefix(out, head) {
		t.Fatal("head retention is not rune-aligned")
	}
	if !utf8.ValidString(out) {
		t.Fatal("pruned output must be valid UTF-8")
	}
}

func TestPrune_BelowThreshold_Unchanged(t *testing.T) {
	m := MustNew(DefaultConfig())
	hook := hookOf(m)
	in := strings.Repeat("x", 100)
	out, _ := runResult(hook, newCtx(""), "tool", in)
	if out != in {
		t.Fatal("short results must pass through untouched")
	}
}

func TestPrune_EmptyResult_Unchanged(t *testing.T) {
	m := MustNew(DefaultConfig())
	hook := hookOf(m)
	out, err := runResult(hook, newCtx(""), "tool", "")
	if err != nil || out != "" {
		t.Fatal("empty results must pass through untouched")
	}
}

// ── spill ───────────────────────────────────────────────────────────────

func TestSpill_StoresOversizedTextAndBoundsPreview(t *testing.T) {
	store := spill.NewMemoryStore()
	m := MustNew(Config{Store: store, MaxInlineBytes: 4096, PruneThresholdChars: 0})
	hook := hookOf(m)
	in := strings.Repeat("payload-", 2000) // 16000 bytes
	out, err := runResult(hook, newCtx("sess-1"), "tool", in)
	if err != nil {
		t.Fatalf("hook error: %v", err)
	}
	if len(out) > m.cfg.MaxInlineBytes {
		t.Fatalf("replacement %d bytes > cap %d", len(out), m.cfg.MaxInlineBytes)
	}
	if !strings.Contains(out, "Full result at:") {
		t.Fatal("replacement must point at the spill locator")
	}
	// The full original must be retrievable from the store under the session owner.
	loc := extractLocator(t, out)
	got, err := store.Retrieve(context.Background(), "sess-1", loc)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if got != in {
		t.Fatal("spilled text must round-trip exactly")
	}
	if _, err := store.Retrieve(context.Background(), "other", loc); err == nil {
		t.Fatal("other owners must not reach the spilled text")
	}
}

func TestSpill_StoreError_KeepsOriginal(t *testing.T) {
	m := MustNew(Config{Store: &errStore{err: fmt.Errorf("disk full")}, MaxInlineBytes: 256, PruneThresholdChars: 0})
	hook := hookOf(m)
	in := strings.Repeat("z", 1000)
	out, err := runResult(hook, newCtx("sess-1"), "tool", in)
	if err != nil {
		t.Fatal("a spill failure must never turn success into a hook error")
	}
	if out != in {
		t.Fatal("a spill failure must keep the original content")
	}
}

func TestSpill_NoAgentContext_KeepsOriginal(t *testing.T) {
	store := spill.NewMemoryStore()
	m := MustNew(Config{Store: store, MaxInlineBytes: 256, PruneThresholdChars: 0})
	hook := hookOf(m)
	in := strings.Repeat("y", 1000)
	out, _ := runResult(hook, context.Background(), "tool", in)
	if out != in {
		t.Fatal("without an owner namespace the spill must be skipped")
	}
}

func TestSpill_SkipTool_KeepsOriginal(t *testing.T) {
	store := spill.NewMemoryStore()
	m := MustNew(Config{Store: store, MaxInlineBytes: 256, PruneThresholdChars: 0})
	hook := hookOf(m)
	in := strings.Repeat("w", 1000)
	out, _ := runResult(hook, newCtx("sess-1"), "read", in)
	if out != in {
		t.Fatal("the read tool must never be spilled (no read→spill→read loop)")
	}
}

func TestSpill_DisabledByZeroCap_KeepsOriginal(t *testing.T) {
	store := spill.NewMemoryStore()
	m := MustNew(Config{Store: store, MaxInlineBytes: 0, PruneThresholdChars: 1000000})
	hook := hookOf(m)
	in := strings.Repeat("v", 100000)
	out, _ := runResult(hook, newCtx("sess-1"), "tool", in)
	if out != in {
		t.Fatal("spill disabled (0 bytes) must pass content through")
	}
}

func TestSpill_PreviewBoundedWithMultibyteText(t *testing.T) {
	store := spill.NewMemoryStore()
	m := MustNew(Config{Store: store, MaxInlineBytes: 512, PruneThresholdChars: 0})
	hook := hookOf(m)
	in := strings.Repeat("汉字", 1000) // 6000 bytes
	out, _ := runResult(hook, newCtx("sess-1"), "tool", in)
	if len(out) > 512 {
		t.Fatalf("multibyte preview %d bytes > cap 512", len(out))
	}
	if !utf8.ValidString(out) {
		t.Fatal("multibyte preview must stay valid UTF-8")
	}
}

// ── pipeline ────────────────────────────────────────────────────────────

func TestPipeline_SpillFirst_StoresFullTextAndBoundsInline(t *testing.T) {
	store := spill.NewMemoryStore()
	// MaxInlineBytes sits between the pruned size and the raw size: the
	// store must hold the FULL text (spill runs before prune), while the
	// inline view stays within the byte cap.
	m := MustNew(Config{Store: store, MaxInlineBytes: 6000})
	hook := hookOf(m)
	in := strings.Repeat("payload-", 3000) // 24000 bytes
	out, _ := runResult(hook, newCtx("sess-1"), "tool", in)
	if len(out) > 6000 {
		t.Fatalf("inline view %d bytes > cap 6000", len(out))
	}
	loc := extractLocator(t, out)
	got, err := store.Retrieve(context.Background(), "sess-1", loc)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if got != in {
		t.Fatal("stored text must be the full original (spill stores before pruning)")
	}
}

func TestPipeline_NoticeSurvivesPruning(t *testing.T) {
	store := spill.NewMemoryStore()
	// Inline cap exceeds the prune threshold: the spilled preview itself
	// gets pruned, but the notice sits in the tail region and must survive.
	m := MustNew(Config{Store: store, MaxInlineBytes: 12000})
	hook := hookOf(m)
	in := strings.Repeat("payload-", 3000) // 24000 bytes
	out, _ := runResult(hook, newCtx("sess-1"), "tool", in)
	if utf8.RuneCountInString(out) > m.cfg.PruneThresholdChars {
		t.Fatalf("inline view %d runes > threshold %d", utf8.RuneCountInString(out), m.cfg.PruneThresholdChars)
	}
	loc := extractLocator(t, out)
	if _, err := store.Retrieve(context.Background(), "sess-1", loc); err != nil {
		t.Fatalf("locator must stay discoverable after pruning: %v", err)
	}
}

// ── config ──────────────────────────────────────────────────────────────

func TestNew_RejectsNonConvergingGeometry(t *testing.T) {
	_, err := New(Config{PruneThresholdChars: 100, PruneHeadChars: 90, PruneTailChars: 80})
	if err == nil {
		t.Fatal("head+marker+tail > threshold must be rejected at construction")
	}
}

func extractLocator(t *testing.T, out string) string {
	t.Helper()
	const needle = "Full result at: "
	i := strings.Index(out, needle)
	if i < 0 {
		t.Fatalf("no locator in %q", out)
	}
	rest := out[i+len(needle):]
	j := strings.Index(rest, ".")
	if j < 0 {
		t.Fatalf("malformed locator tail in %q", out)
	}
	return rest[:j]
}
