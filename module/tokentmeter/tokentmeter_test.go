package tokentmeter

import (
	"testing"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

// TestEstimateMessage_Heuristic: the fixed heuristic prices content and
// structural overhead.
func TestEstimateMessage_Heuristic(t *testing.T) {
	// 16 ASCII chars → 16/4 + 1 = 5 tokens.
	if got := EstimateMessage(types.NewUserMessage("abcdefghijklmnop")); got != 5 {
		t.Fatalf("ascii pricing = %d, want 5", got)
	}
	// Empty message costs the envelope only.
	if got := EstimateMessage(types.NewUserMessage("")); got != 1 {
		t.Fatalf("empty pricing = %d, want 1", got)
	}
	// Nil message costs nothing.
	if got := EstimateMessage(nil); got != 0 {
		t.Fatalf("nil pricing = %d, want 0", got)
	}
	// Tool calls add their surface.
	msg := &types.Message{
		Role: types.RoleAssistant,
		ToolCalls: []types.ToolCall{{
			ID: "call_1", Type: "function",
			Function: types.ToolCallFunction{Name: "read", Arguments: `{"path":"/a"}`},
		}},
	}
	base := EstimateMessage(types.NewAssistantMessage(""))
	withCall := EstimateMessage(msg)
	if withCall <= base {
		t.Fatalf("tool call must add tokens: base=%d with=%d", base, withCall)
	}
}

// TestMeasure_FoldsDerivedSurface: Measure prices the log's derived
// messages — replacements shadow the surface and change the price.
func TestMeasure_FoldsDerivedSurface(t *testing.T) {
	log := coresession.NewLog("s1")
	log.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("aaaaaaaaaaaaaaaa"))) // 16 chars
	log.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("bbbbbbbbbbbbbbbb"))) // 16 chars
	m := New()
	before := m.Measure(log)
	if before != 10 {
		t.Fatalf("measure = %d, want 10 (two 5-token messages)", before)
	}

	// Replace [1,2] with one summary message: the surface shrinks.
	_, err := log.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 1, 2, types.NewUserMessage("cccc"))) // 4 chars → 2 tokens
	if err != nil {
		t.Fatal(err)
	}
	after := m.Measure(log)
	if after != 2 {
		t.Fatalf("post-replace measure = %d, want 2", after)
	}
}

// TestMeasureMessages_Convenience: the message-list convenience matches the
// per-message sum.
func TestMeasureMessages_Convenience(t *testing.T) {
	msgs := []*types.Message{
		types.NewSystemMessage("abcdefgh"),
		types.NewUserMessage("ijklmnop"),
	}
	got := MeasureMessages(msgs)
	if got != EstimateMessage(msgs[0])+EstimateMessage(msgs[1]) {
		t.Fatalf("convenience = %d, want the sum", got)
	}
}
