// Package repeattool guards against repeated tool calls with identical
// (name, normalized args): after a threshold of consecutive repeats, it
// injects an escalating reminder into the message stream. The reminder is
// advice only — the decision stays with the model, and the tool result is
// never rewritten (DSH repeat-tool-reminder).
//
// Package repeattool 防止对相同（工具名、规范化参数）的连续重复调用：
// 达到阈值后向消息流注入逐级升级的提醒。提醒只是建议——决定权始终在
// 模型，工具结果绝不被改写（DSH repeat-tool-reminder）。
package repeattool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Option configures the module.
// Option 配置模块。
type Option func(*Module)

// WithThresholds overrides the escalation thresholds (default [3, 5, 8]).
// The module reminds at each threshold crossing with increasingly detailed
// guidance.
//
// WithThresholds 覆盖升级阈值（默认 [3, 5, 8]）。模块在每次越过阈值时
// 注入越来越详细的提醒。
func WithThresholds(thresholds []int) Option {
	return func(m *Module) { m.thresholds = append([]int(nil), thresholds...) }
}

// WithArgumentsPreviewLen caps the argument preview length in reminders
// (default 200 runes).
// WithArgumentsPreviewLen 限制提醒中参数预览长度（默认 200 字符）。
func WithArgumentsPreviewLen(n int) Option {
	return func(m *Module) { m.previewLen = n }
}

// Module tracks repeat chains per agent invocation (in-memory; chains reset
// on resume — DSH semantics).
// Module 按 agent 执行跟踪重复链（内存态；恢复后链重置——DSH 语义）。
type Module struct {
	mu         sync.Mutex
	chains     map[string]*chain // agent name → chain
	thresholds []int
	previewLen int
	// pending carries reminders produced by OnToolResult until the next
	// OnMessagesBuilt injects them (one per agent per step).
	pending map[string]string
}

type chain struct {
	key      string
	name     string
	args     string
	count    int
	reminded int // highest threshold already reminded
}

// New creates the module with default thresholds [3, 5, 8].
// New 以默认阈值 [3, 5, 8] 创建模块。
func New(opts ...Option) *Module {
	m := &Module{
		chains:     make(map[string]*chain),
		thresholds: []int{3, 5, 8},
		previewLen: 200,
		pending:    make(map[string]string),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Register implements kernel.Module: it hooks tool results to track repeat
// chains and message building to inject reminders.
// Register 实现 kernel.Module：钩住工具结果以跟踪重复链，钩住消息构建以
// 注入提醒。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnToolResult(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		if info == nil || info.Name == "" {
			return ctx, info, nil
		}
		ac := kernel.GetAgentContext(ctx)
		agent := ""
		if ac != nil {
			agent = ac.AgentName()
		}
		if reminder := m.observe(agent, info.Name, info.Args); reminder != "" {
			m.mu.Lock()
			m.pending[agent] = reminder
			m.mu.Unlock()
		}
		return ctx, info, nil
	})
	rt.OnMessagesBuilt(func(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
		ac := kernel.GetAgentContext(ctx)
		agent := ""
		if ac != nil {
			agent = ac.AgentName()
		}
		m.mu.Lock()
		reminder, ok := m.pending[agent]
		if ok {
			delete(m.pending, agent)
		}
		m.mu.Unlock()
		if !ok {
			return ctx, msgs, nil
		}
		// The reminder joins the message stream as a system message.
		// The tool result itself stays untouched (advice, not rewriting).
		// 追加系统消息，由 FireMessagesBuilt 统一合并进 system。
		return ctx, append(msgs, types.NewSystemMessage(reminder)), nil
	})
}

// observe updates the repeat chain and returns the reminder text to inject,
// or "" when no threshold was crossed.
func (m *Module) observe(agent, name, args string) string {
	key := name + "\x00" + normalizeArgs(args)
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.chains[agent]
	if c == nil {
		c = &chain{key: key, name: name, args: args}
		m.chains[agent] = c
	}
	if c.key != key {
		// Different call: reset the chain.
		c.key = key
		c.name = name
		c.args = args
		c.count = 1
		c.reminded = 0
		return ""
	}
	c.count++
	for _, th := range m.thresholds {
		if c.count == th {
			c.reminded = th
			return m.render(c, th)
		}
	}
	return ""
}

func (m *Module) render(c *chain, threshold int) string {
	preview := truncate(c.args, m.previewLen)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Note: tool %q has been called %d times in a row with the same arguments", c.name, threshold))
	if preview != "" {
		b.WriteString(fmt.Sprintf(" (%s)", preview))
	}
	b.WriteString(". Check whether the previous result actually failed or whether a different approach is needed before calling it again.")
	return b.String()
}

// normalizeArgs canonicalizes the arguments JSON for repeat comparison:
// keys are sorted, whitespace is insignificant — two calls with the same
// logical arguments count as repeats even when the raw text differs.
func normalizeArgs(args string) string {
	var v any
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return args // unparseable args compare verbatim
	}
	b, err := json.Marshal(canonical(v))
	if err != nil {
		return args
	}
	return string(b)
}

// canonical deep-sorts map keys for stable JSON comparison.
func canonical(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = canonical(t[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = canonical(item)
		}
		return out
	default:
		return v
	}
}

// truncate keeps the head of a string within n runes.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
