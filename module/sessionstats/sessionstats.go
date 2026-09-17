// Package sessionstats provides the session-stats projection unit (DSH
// session-stats): it folds the session event log into derived usage
// statistics — steps, turns, token usage, and timing — served to consumers
// (GUI trajectory pages, usage dashboards, exports). It plugs into
// module/projection as a Unit, so the registry drives it and the change
// feed publishes updates.
//
// Package sessionstats 提供会话统计投影单元（DSH session-stats）：把会话
// 事件日志折叠为派生用量统计——步骤、轮次、token 用量与耗时——供消费方
// （GUI 轨迹页、用量看板、导出）使用。它作为 Unit 接入
// module/projection，由注册表驱动、变更馈送发布更新。
package sessionstats

import (
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

// Stats is the derived read model.
// Stats 是派生的读模型。
type Stats struct {
	Steps        int           `json:"steps"`
	Turns        int           `json:"turns"`
	PromptTokens int           `json:"prompt_tokens"`
	OutputTokens int           `json:"output_tokens"`
	TotalTokens  int           `json:"total_tokens"`
	ToolCalls    int           `json:"tool_calls"`
	FirstTokenMs int64         `json:"first_token_ms,omitempty"`
	TotalMs      int64         `json:"total_ms,omitempty"`
	StartedAt    time.Time     `json:"started_at,omitempty"`
	EndedAt      time.Time     `json:"ended_at,omitempty"`
}

// Unit implements projection.Unit over the session log.
// Unit 在会话日志上实现 projection.Unit。
type Unit struct {
	mu    sync.Mutex
	stats Stats
}

// Key implements projection.Unit.
// Key 实现 projection.Unit：返回固定的统计键。
func (u *Unit) Key() string { return "session_stats" }

// Reset implements projection.Unit: a fresh session starts empty.
// Reset 实现 projection.Unit：新会话从空统计开始。
func (u *Unit) Reset() {
	u.mu.Lock()
	u.stats = Stats{}
	u.mu.Unlock()
}

// Apply implements projection.Unit: fold one committed event into the
// statistics.
//
// Apply 实现 projection.Unit：把一条已提交事件折叠进统计。
func (u *Unit) Apply(ev types.SessionEvent) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := &u.stats
	switch ev.Kind {
	case "step/start":
		if s.Steps == 0 {
			s.StartedAt = ev.At
		}
		s.Steps++
	case "step/end":
		s.EndedAt = ev.At
		// Token usage rides on the step-end event when the producer
		// attached it (the LogWriter → log → stats chain).
		if usage := usageOf(ev); usage != nil {
			s.PromptTokens += usage.PromptTokens
			s.OutputTokens += usage.CompletionTokens
			s.TotalTokens += usage.TotalTokens
		}
	case "turn/start":
		s.Turns++
	case "session/tool_call":
		s.ToolCalls++
	case "session/usage":
		// The cumulative usage checkpoint (logSession.AddTokenUsage): a
		// last-wins total, not a delta.
		if v, ok := ev.Meta["used_tokens"]; ok {
			switch n := v.(type) {
			case float64:
				s.TotalTokens = int(n)
			case int:
				s.TotalTokens = n
			}
		}
	case "session/model_usage":
		// Per-call usage deltas (LogWriter.onModelResult): accumulate.
		if u := usageOf(ev); u != nil {
			s.PromptTokens += u.PromptTokens
			s.OutputTokens += u.CompletionTokens
			s.TotalTokens += u.TotalTokens
		}
	case types.SessionEventAssistantMessage:
		// Usage may ride on the assistant message meta.
		if u := usageOf(ev); u != nil {
			s.PromptTokens += u.PromptTokens
			s.OutputTokens += u.CompletionTokens
			s.TotalTokens += u.TotalTokens
		}
	}
	if !s.StartedAt.IsZero() && !s.EndedAt.IsZero() {
		s.TotalMs = s.EndedAt.Sub(s.StartedAt).Milliseconds()
	}
}

// View implements projection.Unit: a copy of the stats.
// View 实现 projection.Unit：返回统计的副本。
func (u *Unit) View() any {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.stats
	return &s
}

// usageOf extracts the token usage from an event's meta when present. The
// values may arrive as float64 (after a JSON round-trip) or int (written
// in-memory) — both are accepted.
func usageOf(ev types.SessionEvent) *types.TokenUsage {
	if ev.Meta == nil {
		return nil
	}
	u := &types.TokenUsage{}
	ok := false
	if v, ok1 := ev.Meta["prompt_tokens"].(float64); ok1 {
		u.PromptTokens = int(v)
		ok = true
	} else if v, ok1 := ev.Meta["prompt_tokens"].(int); ok1 {
		u.PromptTokens = v
		ok = true
	}
	if v, ok1 := ev.Meta["completion_tokens"].(float64); ok1 {
		u.CompletionTokens = int(v)
		ok = true
	} else if v, ok1 := ev.Meta["completion_tokens"].(int); ok1 {
		u.CompletionTokens = v
		ok = true
	}
	if v, ok1 := ev.Meta["total_tokens"].(float64); ok1 {
		u.TotalTokens = int(v)
		ok = true
	} else if v, ok1 := ev.Meta["total_tokens"].(int); ok1 {
		u.TotalTokens = v
		ok = true
	}
	if !ok {
		return nil
	}
	return u
}

// NewUnit creates a fresh stats unit.
// NewUnit 创建全新统计单元。
func NewUnit() *Unit {
	return &Unit{}
}
