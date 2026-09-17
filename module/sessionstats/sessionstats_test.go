package sessionstats

import (
	"testing"
	"time"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/module/projection"
	"github.com/go-gocel/gocel/core/types"
)

func at(t time.Time) types.SessionEvent {
	return types.SessionEvent{At: t}
}

// TestUnit_FoldsStats: the unit folds steps/turns/tool calls/usage from the
// log, and the projection registry serves the derived read model.
func TestUnit_FoldsStats(t *testing.T) {
	log := coresession.NewLog("s1")
	reg := projection.New()
	unit := NewUnit()
	if err := reg.Register(unit); err != nil {
		t.Fatal(err)
	}
	detach, err := reg.Attach(log)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()

	base := time.Now().UTC()
	log.Append(at(base)) // step/start without kind: ignored
	log.Append(types.NewLogOnlyEvent("step/start", nil))
	log.Append(types.NewLogOnlyEvent("turn/start", nil))
	log.Append(types.NewLogOnlyEvent("session/tool_call", nil))
	log.Append(types.NewSessionEvent(types.SessionEventAssistantMessage, nil))
	log.Append(types.NewLogOnlyEvent("step/end", nil))

	snap := reg.Snapshot("s1")
	st, ok := snap["session_stats"].(*Stats)
	if !ok {
		t.Fatalf("snapshot = %+v, want session_stats", snap)
	}
	if st.Steps != 1 || st.Turns != 1 || st.ToolCalls != 1 {
		t.Fatalf("stats = %+v, want 1 step / 1 turn / 1 tool call", st)
	}
	if st.TotalTokens != 0 {
		t.Fatalf("stats = %+v, want zero usage without usage events", st)
	}
}

// TestUnit_UsageAndTiming: usage events accumulate; timing spans
// start→end; the cumulative session/usage checkpoint is folded.
func TestUnit_UsageAndTiming(t *testing.T) {
	u := NewUnit()
	u.Apply(types.NewLogOnlyEvent("step/start", nil))
	u.Apply(types.NewSessionEvent(types.SessionEventAssistantMessage, nil))
	// Attach usage via meta on a step/end event (the LogWriter shape).
	u.Apply(types.NewLogOnlyEvent("step/end", map[string]any{
		"prompt_tokens":    100,
		"completion_tokens": 50,
		"total_tokens":     150,
	}))
	u.Apply(types.NewLogOnlyEvent("step/end", nil))
	// Cumulative usage checkpoint (logSession.AddTokenUsage shape).
	u.Apply(types.NewLogOnlyEvent("session/usage", map[string]any{"used_tokens": 42}))

	view := u.View().(*Stats)
	// step/end deltas accumulate (100/50/150); the later session/usage
	// checkpoint is a cumulative last-wins total that supersedes the sum.
	if view.PromptTokens != 100 || view.OutputTokens != 50 || view.TotalTokens != 42 {
		t.Fatalf("usage = %+v, want prompt 100 / output 50 / total 42 (usage checkpoint wins)", view)
	}
	if view.TotalMs < 0 {
		t.Fatalf("timing = %+v, want non-negative", view)
	}
}

// TestUnit_ResetClears: Reset returns the unit to a fresh state.
func TestUnit_ResetClears(t *testing.T) {
	u := NewUnit()
	u.Apply(types.NewLogOnlyEvent("step/start", nil))
	u.Apply(types.NewLogOnlyEvent("turn/start", nil))
	u.Reset()
	v := u.View().(*Stats)
	if v.Steps != 0 || v.Turns != 0 {
		t.Fatalf("after reset = %+v, want empty", v)
	}
}
