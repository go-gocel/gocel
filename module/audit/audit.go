// Package audit provides a gocel Module that records tool, model, run and
// guard-decision events as structured audit records.
//
// 双通道输出：文本（默认 stderr，人读）与 JSON lines（WithWriter，机器读），
// 可同时开启。每条记录都携带 InvocationID/AgentName/StepIndex 关联字段
// （由 Runtime 在触发钩子时统一填充），可直接串起「运行 → 步骤 → 调用」链。
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Logger is the text sink. It matches the standard library log.Logger.
//
// Logger 是文本输出通道，与标准库 log.Logger 接口一致。
type Logger interface {
	Printf(format string, v ...any)
}

// Event is a single structured audit record written to the JSON lines sink.
//
// Event 是写入 JSON lines 输出通道的单条结构化审计记录。
type Event struct {
	Time time.Time `json:"time"`
	// Kind 标注事件来源：tool=工具调用，model=模型调用，run=运行级，decision=守卫决策。
	Kind string `json:"kind"`
	// Status 细化事件状态：start / done / error（decision 事件无 status）。
	Status string `json:"status,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	// Decision 是守卫决策：approved / rejected / hard_blocked / policy_deny /
	// timeout_rejected / timeout_skip / timeout_approve 等。
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`

	InvocationID string `json:"invocation_id,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
	// StepIndex 是触发事件的步骤序号；非步骤级事件（run 级）为 -1。
	StepIndex int `json:"step_index"`

	// Model call fields.
	Messages         int `json:"messages,omitempty"`
	EstTokens        int `json:"est_tokens,omitempty"`
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

// ── Options ──────────────────────────────────────────────────────────────

type config struct {
	logger       Logger
	writer       io.Writer
	maxArgsLen   int
	maxResultLen int
	maxErrorLen  int
	sampleRate   float64
}

// Option configures the audit Module.
//
// Option 配置审计 Module。
type Option func(*config)

// WithLogger sets the human-readable text sink. The default (stderr) applies
// only when neither a logger nor a writer is configured.
//
// WithLogger 设置人类可读的文本输出通道。仅当既未配置 logger 也未配置
// writer 时才使用默认的 stderr。
func WithLogger(l Logger) Option { return func(c *config) { c.logger = l } }

// WithWriter enables the JSON lines sink. Audit failures only affect the
// record, never the agent loop.
//
// WithWriter 启用 JSON lines 输出通道。审计失败只影响记录本身，绝不阻断
// agent 循环。
func WithWriter(w io.Writer) Option { return func(c *config) { c.writer = w } }

// WithMaxArgsLen caps the recorded args length (runes); <= 0 disables truncation.
//
// WithMaxArgsLen 限制记录的参数长度（字符数）；<= 0 表示不截断。
func WithMaxArgsLen(n int) Option { return func(c *config) { c.maxArgsLen = n } }

// WithMaxResultLen caps the recorded result length (runes); <= 0 disables truncation.
//
// WithMaxResultLen 限制记录的结果长度（字符数）；<= 0 表示不截断。
func WithMaxResultLen(n int) Option { return func(c *config) { c.maxResultLen = n } }

// WithMaxErrorLen caps the recorded error length (runes); <= 0 disables truncation.
//
// WithMaxErrorLen 限制记录的错误长度（字符数）；<= 0 表示不截断。
func WithMaxErrorLen(n int) Option { return func(c *config) { c.maxErrorLen = n } }

// WithSampleRate sets the record fraction in [0,1]. 1 (default) records
// everything; 0 records nothing.
//
// WithSampleRate 设置记录比例 [0,1]。1（默认）记录全部；0 不记录。
func WithSampleRate(rate float64) Option { return func(c *config) { c.sampleRate = rate } }

// DefaultMaxArgsLen is the default cap on recorded argument length (runes),
// balancing audit retention against log flooding.
//
// DefaultMaxArgsLen 是记录参数长度的默认上限（字符数），在留痕与防刷屏之间
// 取平衡（与消费层历史实践一致）。
const (
	DefaultMaxArgsLen   = 2000
	DefaultMaxResultLen = 4000
	DefaultMaxErrorLen  = 2000
)

// ── Module ───────────────────────────────────────────────────────────────

// Module records tool/model/run/decision events to the configured sinks.
//
// Module 把工具/模型/运行/决策事件记录到配置的输出通道。
type Module struct {
	cfg config
	mu  sync.Mutex // serializes writer access and the sampling counter

	sampled uint64
	emitted uint64
}

// New creates an audit Module. With no options it logs human-readable text
// to stderr. Add WithWriter for machine-readable JSON lines.
//
// New 创建审计 Module。无选项时向 stderr 输出人类可读文本；
// 添加 WithWriter 可输出机器可读的 JSON lines。
func New(opts ...Option) *Module {
	cfg := config{
		maxArgsLen:   DefaultMaxArgsLen,
		maxResultLen: DefaultMaxResultLen,
		maxErrorLen:  DefaultMaxErrorLen,
		sampleRate:   1,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.logger == nil && cfg.writer == nil {
		cfg.logger = log.New(os.Stderr, "[audit] ", log.LstdFlags)
	}
	if cfg.sampleRate < 0 {
		cfg.sampleRate = 0
	}
	if cfg.sampleRate > 1 {
		cfg.sampleRate = 1
	}
	return &Module{cfg: cfg}
}

// Register implements kernel.Module.
//
// Register 实现 kernel.Module 接口，注册全部审计钩子。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnToolCall(m.onToolCall)
	rt.OnToolResult(m.onToolResult)
	rt.OnModelCall(m.onModelCall)
	rt.OnModelResult(m.onModelResult)
	rt.OnAgentEnd(m.onAgentEnd)
	rt.OnDecision(m.onDecision)
}

// ── Hooks ────────────────────────────────────────────────────────────────

func (m *Module) onToolCall(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	m.emit(Event{
		Kind: "tool", Status: "start",
		Tool:         info.Name,
		Args:         truncate(info.Args, m.cfg.maxArgsLen),
		InvocationID: info.InvocationID,
		AgentName:    info.AgentName,
		StepIndex:    info.StepIndex,
	})
	return ctx, info, nil
}

func (m *Module) onToolResult(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	status := "done"
	errText := ""
	if info.Error != nil {
		status = "error"
		errText = info.Error.Error()
	} else if isErrorPayload(info.Result) {
		status = "error"
		errText = info.Result
	}
	m.emit(Event{
		Kind: "tool", Status: status,
		Tool:         info.Name,
		Args:         truncate(info.Args, m.cfg.maxArgsLen),
		Result:       truncate(info.Result, m.cfg.maxResultLen),
		Error:        truncate(errText, m.cfg.maxErrorLen),
		InvocationID: info.InvocationID,
		AgentName:    info.AgentName,
		StepIndex:    info.StepIndex,
	})
	return ctx, info, nil
}

func (m *Module) onModelCall(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	m.emit(Event{
		Kind: "model", Status: "start",
		Messages:     len(info.Messages),
		EstTokens:    types.EstimateTokens(info.Messages),
		InvocationID: info.InvocationID,
		AgentName:    info.AgentName,
		StepIndex:    info.StepIndex,
	})
	return ctx, info, nil
}

func (m *Module) onModelResult(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	ev := Event{
		Kind: "model", Status: "done",
		Messages:     len(info.Messages),
		InvocationID: info.InvocationID,
		AgentName:    info.AgentName,
		StepIndex:    info.StepIndex,
	}
	if info.Error != nil {
		ev.Status = "error"
		ev.Error = truncate(info.Error.Error(), m.cfg.maxErrorLen)
	} else if info.Usage != nil {
		ev.PromptTokens = info.Usage.PromptTokens
		ev.CompletionTokens = info.Usage.CompletionTokens
		ev.TotalTokens = info.Usage.TotalTokens
	}
	m.emit(ev)
	return ctx, info, nil
}

// onAgentEnd records a run-level audit entry: agent, message count, outcome.
func (m *Module) onAgentEnd(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
	status := "done"
	errText := ""
	if info.Err != nil {
		status = "error"
		errText = info.Err.Error()
	}
	tokens := 0
	if info.Result != nil && info.Result.TokenUsage != nil {
		tokens = info.Result.TokenUsage.TotalTokens
	}
	m.emit(Event{
		Kind: "run", Status: status,
		Error:        truncate(errText, m.cfg.maxErrorLen),
		InvocationID: info.InvocationID,
		AgentName:    info.AgentName,
		StepIndex:    -1,
		Messages:     len(info.AllMsgs),
		TotalTokens:  tokens,
	})
	return ctx, info, nil
}

func (m *Module) onDecision(ctx context.Context, info *kernel.DecisionInfo) (context.Context, *kernel.DecisionInfo, error) {
	if info == nil {
		return ctx, info, nil
	}
	m.emit(Event{
		Kind:         "decision",
		Tool:         info.Tool,
		Args:         truncate(info.Args, m.cfg.maxArgsLen),
		Decision:     info.Decision,
		Reason:       info.Reason,
		InvocationID: info.InvocationID,
		AgentName:    info.AgentName,
		StepIndex:    info.StepIndex,
	})
	return ctx, info, nil
}

// ── Emission ─────────────────────────────────────────────────────────────

// emit applies sampling and writes the event to the text and JSON sinks.
// Sampling is deterministic: with rate r, roughly the r fraction of events
// passes (the first event always passes when r > 0).
func (m *Module) emit(ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sampled++
	if m.cfg.sampleRate < 1 && float64(m.sampled)*m.cfg.sampleRate <= float64(m.emitted) {
		return
	}
	m.emitted++

	ev.Time = ev.Time.UTC()
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}

	if m.cfg.logger != nil {
		m.cfg.logger.Printf("%s", textLine(ev))
	}
	if m.cfg.writer != nil {
		line, err := json.Marshal(ev)
		if err != nil {
			// 审计序列化失败只影响留痕，不阻断 agent 循环。
			if m.cfg.logger != nil {
				m.cfg.logger.Printf("AUDIT ERROR  marshal: %v", err)
			}
			return
		}
		if _, err := m.cfg.writer.Write(append(line, '\n')); err != nil && m.cfg.logger != nil {
			m.cfg.logger.Printf("AUDIT ERROR  write: %v", err)
		}
	}
}

// textLine renders a human-readable line in the classic audit format.
func textLine(ev Event) string {
	switch ev.Kind {
	case "tool":
		switch ev.Status {
		case "start":
			return fmt.Sprintf("TOOL START  %s", ev.Tool)
		case "error":
			return fmt.Sprintf("TOOL ERROR  %s: %s", ev.Tool, ev.Error)
		default:
			return fmt.Sprintf("TOOL DONE   %s (%d chars)", ev.Tool, len(ev.Result))
		}
	case "model":
		switch ev.Status {
		case "start":
			return fmt.Sprintf("MODEL START %d messages, ~%d tokens", ev.Messages, ev.EstTokens)
		case "error":
			return fmt.Sprintf("MODEL ERROR %s", ev.Error)
		default:
			return fmt.Sprintf("MODEL DONE  prompt=%d completion=%d total=%d",
				ev.PromptTokens, ev.CompletionTokens, ev.TotalTokens)
		}
	case "run":
		status := "DONE"
		if ev.Status == "error" {
			status = "ERROR"
		}
		return fmt.Sprintf("RUN %-5s agent=%s msgs=%d tokens=%d", status, ev.AgentName, ev.Messages, ev.TotalTokens)
	case "decision":
		return fmt.Sprintf("DECISION    %-14s tool=%s reason=%s", ev.Decision, ev.Tool, ev.Reason)
	}
	return fmt.Sprintf("AUDIT %s %s", ev.Kind, ev.Status)
}

// ── Helpers ──────────────────────────────────────────────────────────────

// truncate keeps the first n runes; overlength strings get an ellipsis.
// n <= 0 means no truncation.
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

// isErrorPayload reports whether tool result content is a failure payload.
// gocel convention: tool failures (guard block/exec error/result rejection)
// carry a JSON object with an "error" field as the result content.
func isErrorPayload(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), `{"error":`)
}
