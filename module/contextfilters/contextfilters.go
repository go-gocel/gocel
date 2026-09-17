// Package contextfilters keeps tool results inside the model's context
// budget: the pruner truncates oversized results (DSH tool-result-pruner
// parameters and marker), and the spill policy stores oversized pure-text
// results in a spill store, replacing the message content with a bounded
// preview pointing at the full text.
//
// Failure semantics (DSH spill-policy): a spill failure NEVER turns a
// successful tool call into an error — the original content is kept.
//
// Package contextfilters 把工具结果限制在模型上下文预算内：pruner 截断
// 超限结果（DSH tool-result-pruner 参数与标记），spill 策略把超限纯文本
// 结果存入落盘存储，消息内容替换为指向全文的有界预览。
//
// 失败语义（DSH spill-policy）：spill 失败绝不把成功的工具调用变成错误
// ——原文保留。
package contextfilters

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/spill"
)

// PruneMarker marks the dropped middle of a pruned result.
//
// PruneMarker 标记被剪裁结果中被丢弃的中间部分。
const PruneMarker = "[... middle pruned ...]"

// Config tunes the pipeline.
//
// Config 调整处理管线参数。
type Config struct {
	// Store is the spill store; nil disables spilling.
	Store spill.Store
	// MaxInlineBytes triggers spilling for larger pure-text results;
	// 0 = spill disabled (DSH: threshold omitted = no-op).
	MaxInlineBytes int
	// PruneThresholdChars triggers pruning for longer results (runes);
	// 0 uses the default (8192, DSH) — pruning stays on by default.
	PruneThresholdChars int
	// PruneHeadChars / PruneTailChars keep the head and tail of a pruned
	// result. Defaults 4096 / 1024 (DSH).
	PruneHeadChars int
	PruneTailChars int
	// SkipTools never spills (DSH skips `read`: no read→spill→read loop).
	SkipTools []string
}

// DefaultConfig returns the DSH-verified defaults (prune on, spill off).
//
// DefaultConfig 返回经 DSH 验证的默认配置（启用剪裁、关闭 spill）。
func DefaultConfig() Config {
	return Config{
		PruneThresholdChars: 8192,
		PruneHeadChars:      4096,
		PruneTailChars:      1024,
		SkipTools:           []string{"read"},
	}
}

// Module applies the pipeline to every tool result.
//
// Module 对每个工具结果应用处理管线。
type Module struct {
	cfg Config
}

// New builds the module, validating the pruning geometry: head + marker +
// tail must not exceed the threshold (a single prune must converge).
//
// New 构建模块并校验剪裁几何：head + marker + tail 不得超过阈值
// （一次剪裁必须收敛）。
func New(cfg Config) (*Module, error) {
	if cfg.PruneThresholdChars == 0 {
		cfg.PruneThresholdChars = DefaultConfig().PruneThresholdChars
	}
	if cfg.PruneHeadChars == 0 {
		cfg.PruneHeadChars = DefaultConfig().PruneHeadChars
	}
	if cfg.PruneTailChars == 0 {
		cfg.PruneTailChars = DefaultConfig().PruneTailChars
	}
	marker := utf8.RuneCountInString(PruneMarker)
	if cfg.PruneHeadChars+marker+cfg.PruneTailChars > cfg.PruneThresholdChars {
		return nil, fmt.Errorf("contextfilters: head(%d)+marker(%d)+tail(%d) exceeds threshold(%d)",
			cfg.PruneHeadChars, marker, cfg.PruneTailChars, cfg.PruneThresholdChars)
	}
	if len(cfg.SkipTools) == 0 {
		cfg.SkipTools = DefaultConfig().SkipTools
	}
	return &Module{cfg: cfg}, nil
}

// MustNew builds the module, panicking on configuration errors.
//
// MustNew 构建模块，配置错误时 panic。
func MustNew(cfg Config) *Module {
	m, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return m
}

// Register implements kernel.Module, wiring the spill-then-prune pipeline
// to the OnToolResult hook.
//
// Register 实现 kernel.Module，把 spill-再-prune 管线挂接到 OnToolResult 钩子。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnToolResult(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		if info == nil || info.Result == "" {
			return ctx, info, nil
		}
		// DSH order: spill first (store the FULL text), then prune the
		// inline view — the stored artifact must never be truncated.
		info.Result = m.spill(ctx, info.Name, info.Result)
		info.Result = m.prune(info.Result)
		return ctx, info, nil
	})
}

// prune truncates a result over the rune threshold, keeping head and tail
// around the marker (codepoint counting — never splitting a rune).
func (m *Module) prune(s string) string {
	th := m.cfg.PruneThresholdChars
	if th <= 0 || utf8.RuneCountInString(s) <= th {
		return s
	}
	head := cutRunes(s, m.cfg.PruneHeadChars)
	tail := cutRunesRight(s, m.cfg.PruneTailChars)
	return head + PruneMarker + tail
}

// spill stores oversized pure-text results and replaces the content with a
// bounded preview. Any failure keeps the original text.
func (m *Module) spill(ctx context.Context, toolName, s string) string {
	if m.cfg.Store == nil || m.cfg.MaxInlineBytes <= 0 || len(s) <= m.cfg.MaxInlineBytes {
		return s
	}
	for _, skip := range m.cfg.SkipTools {
		if toolName == skip {
			return s
		}
	}
	owner := ""
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		owner = ac.Facts().SessionID
	}
	ref, err := m.cfg.Store.SaveText(ctx, owner, s)
	if err != nil || owner == "" {
		// A spill failure never turns a successful tool call into an
		// error — the original content stays (DSH semantics).
		return s
	}
	notice := fmt.Sprintf("\n(Omitted %d bytes. Full result at: %s. %s)", ref.Bytes, ref.Locator, ref.Hint)
	previewBudget := m.cfg.MaxInlineBytes - len(notice)
	const mid = "\n[...]\n"
	if previewBudget-len(mid) < 256 {
		// Replacing would exceed the cap: skip entirely, never enlarge.
		return s
	}
	budget := previewBudget - len(mid)
	half := budget / 2
	preview := headBytes(s, half) + mid + tailBytes(s, budget-half)
	return preview + notice
}

func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

func cutRunesRight(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}

func headBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	// Cut at a rune boundary.
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func tailBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// Ensure the module contract stays honest: it mutates only tool results.
var _ kernel.Module = (*Module)(nil)
