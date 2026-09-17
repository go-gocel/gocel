// Package tokentmeter provides the shared token-measurement service (DSH
// token-meter): one replay-aware fold over the session log that prices the
// derived surface, shared by guard (compaction pressure), UI (occupancy),
// and any pressure-sensitive consumer. The estimator uses one fixed
// heuristic — four characters per token plus structural overhead — and
// reuses provider-reported usage when the latest call's envelope matches
// the measured surface.
//
// Package tokentmeter 提供共享 token 计量服务（DSH token-meter）：对会话
// 日志做一次重放感知折叠，为派生 surface 定价——供 guard（压缩压力）、
// UI（占用率）与一切压力敏感消费方共用。估算器使用一个固定启发式——
// 每 token 四个字符加结构开销——并在最新调用信封与测量 surface 匹配时
// 复用 provider 上报的用量。
package tokentmeter

import (
	"sync"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

// Meter measures token pressure from a session log.
// Meter 从会话日志测量 token 压力。
type Meter struct {
	mu sync.Mutex
}

// New creates a meter.
// New 创建计量器。
func New() *Meter { return &Meter{} }

// EstimateMessage prices one message with the fixed heuristic.
// EstimateMessage 用固定启发式给一条消息定价。
func EstimateMessage(m *types.Message) int {
	if m == nil {
		return 0
	}
	// 4 chars/token + structural overhead for the role envelope.
	total := 0
	for _, r := range m.Content {
		if r < 128 {
			total++
		} else {
			total += 2 // CJK etc. are denser
		}
	}
	tokens := total/4 + 1
	if len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			tokens += len(tc.Function.Name)/4 + len(tc.Function.Arguments)/4 + 1
		}
	}
	if m.ToolCallID != "" {
		tokens += len(m.ToolCallID)/4 + 1
	}
	return tokens
}

// Measure prices the current derived surface of the log: the sum of the
// per-message heuristic over DeriveMessages, plus the system prompt and
// tool schemas when carried in the log's system messages.
//
// Measure 为日志的当前派生 surface 定价：DeriveMessages 上逐消息启发式
// 之和，加上日志系统消息携带的系统提示与工具 schema。
func (m *Meter) Measure(log *coresession.Log) int {
	if log == nil {
		return 0
	}
	total := 0
	for _, msg := range log.DeriveMessages() {
		total += EstimateMessage(msg)
	}
	return total
}

// MeasureMessages prices an arbitrary message list (a convenience for
// callers holding messages without a log).
//
// MeasureMessages 为任意消息列表定价（为持有消息但无日志的调用方提供
// 便利）。
func MeasureMessages(msgs []*types.Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateMessage(m)
	}
	return total
}
