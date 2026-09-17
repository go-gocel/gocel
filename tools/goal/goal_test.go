package goal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/goal"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
)

func setup(t *testing.T) (*goal.Manager, []kernel.Tool) {
	t.Helper()
	m := goal.NewManager(goal.NewMemoryStore())
	ts, err := Tools(Config{Manager: m})
	if err != nil {
		t.Fatal(err)
	}
	return m, ts
}

func byName(ts []kernel.Tool, name string) kernel.Tool {
	for _, t := range ts {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

// hostCtx returns a context with an AgentContext whose run state marks the
// run as host-initiated (or not).
func hostCtx(turn bool) context.Context {
	ac := runtime.NewAgentContext()
	ac.State().Set(HostTurnKey, turn)
	return kernel.WithAgentContext(context.Background(), ac)
}

func call(t *testing.T, ts []kernel.Tool, name, args string, ctx context.Context) (string, error) {
	t.Helper()
	tc := byName(ts, name)
	if tc == nil {
		t.Fatalf("tool %q not found", name)
	}
	return tc.Run(ctx, args)
}

func mustCall(t *testing.T, ts []kernel.Tool, name, args string, ctx context.Context) string {
	t.Helper()
	out, err := call(t, ts, name, args, ctx)
	if err != nil {
		t.Fatalf("%s(%s) = %v", name, args, err)
	}
	return out
}

func TestCreate_RequiresHostTurn(t *testing.T) {
	_, ts := setup(t)
	if _, err := call(t, ts, "create_goal", `{"objective":"x","max_rounds":0}`, hostCtx(false)); err == nil || !strings.Contains(err.Error(), "host turn") {
		t.Fatalf("create without host turn = %v, want host-turn denial", err)
	}
	out := mustCall(t, ts, "create_goal", `{"objective":"ship it","max_rounds":2}`, hostCtx(true))
	var g struct {
		ID        string `json:"id"`
		Objective string `json:"objective"`
		Phase     string `json:"phase"`
		Revision  int64  `json:"revision"`
	}
	if err := json.Unmarshal([]byte(out), &g); err != nil {
		t.Fatalf("create output %q: %v", out, err)
	}
	if g.ID == "" || g.Objective != "ship it" || g.Phase != "active" || g.Revision != 1 {
		t.Fatalf("create output = %+v, want active goal rev1", g)
	}
}

func TestGet_ReadableWithoutHostTurn(t *testing.T) {
	_, ts := setup(t)
	out := mustCall(t, ts, "create_goal", `{"objective":"inspect me","max_rounds":0}`, hostCtx(true))
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(out), &created)

	got := mustCall(t, ts, "get_goal", `{"id":"`+created.ID+`"}`, hostCtx(false))
	if !strings.Contains(got, `"inspect me"`) {
		t.Fatalf("get output = %q, want objective", got)
	}
}

func TestUpdate_HostOperationsGated(t *testing.T) {
	_, ts := setup(t)
	out := mustCall(t, ts, "create_goal", `{"objective":"x","max_rounds":0}`, hostCtx(true))
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(out), &created)
	id := created.ID

	for _, action := range []string{"pause", "resume", "complete", "clear", "edit"} {
		if _, err := call(t, ts, "update_goal", `{"id":"`+id+`","action":"`+action+`","reason":"r","objective":"new"}`, hostCtx(false)); err == nil || !strings.Contains(err.Error(), "host turn") {
			t.Fatalf("update %s without host turn = %v, want host-turn denial", action, err)
		}
	}
}

func TestUpdate_PauseResumeCompleteFlow(t *testing.T) {
	m, ts := setup(t)
	out := mustCall(t, ts, "create_goal", `{"objective":"x","max_rounds":0}`, hostCtx(true))
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(out), &created)
	id := created.ID

	host := hostCtx(true)
	mustCall(t, ts, "update_goal", `{"id":"`+id+`","action":"pause","reason":""}`, host)
	g, err := m.Get(context.Background(), id)
	if err != nil || g.Phase != goal.PhasePaused {
		t.Fatalf("after pause = %+v, %v; want paused", g, err)
	}
	mustCall(t, ts, "update_goal", `{"id":"`+id+`","action":"resume","reason":""}`, host)
	mustCall(t, ts, "update_goal", `{"id":"`+id+`","action":"complete","reason":""}`, host)
	g, _ = m.Get(context.Background(), id)
	if g.Phase != goal.PhaseComplete {
		t.Fatalf("after complete = %v, want complete", g.Phase)
	}
}

func TestUpdate_BlockAutonomyThreshold(t *testing.T) {
	m, ts := setup(t)
	out := mustCall(t, ts, "create_goal", `{"objective":"x","max_rounds":0}`, hostCtx(true))
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(out), &created)
	id := created.ID

	// A round can never block an active goal.
	if _, err := call(t, ts, "update_goal", `{"id":"`+id+`","action":"block","reason":"flaky"}`, hostCtx(false)); !errors.Is(err, goal.ErrInvalidTransition) {
		t.Fatalf("autonomous block on active = %v, want ErrInvalidTransition", err)
	}
	// Host blocks first (streak 1); autonomous rounds may only continue the
	// same blocker past the threshold (default 3).
	mustCall(t, ts, "update_goal", `{"id":"`+id+`","action":"block","reason":"flaky"}`, hostCtx(true))
	if _, err := call(t, ts, "update_goal", `{"id":"`+id+`","action":"block","reason":"flaky"}`, hostCtx(false)); !errors.Is(err, goal.ErrInvalidTransition) {
		t.Fatalf("autonomous block at streak 1 = %v, want ErrInvalidTransition", err)
	}
	// Host (or prior rounds) extend to streak 2; the third confirmation may
	// be autonomous.
	mustCall(t, ts, "update_goal", `{"id":"`+id+`","action":"block","reason":"flaky"}`, hostCtx(true))
	mustCall(t, ts, "update_goal", `{"id":"`+id+`","action":"block","reason":"flaky"}`, hostCtx(false))
	g, _ := m.Get(context.Background(), id)
	if g.BlockedStreak != 3 {
		t.Fatalf("blocked streak = %d, want 3", g.BlockedStreak)
	}
	// A different reason can never be reported autonomously.
	if _, err := call(t, ts, "update_goal", `{"id":"`+id+`","action":"block","reason":"other"}`, hostCtx(false)); !errors.Is(err, goal.ErrInvalidTransition) {
		t.Fatalf("autonomous block with new reason = %v, want ErrInvalidTransition", err)
	}
}

func TestUpdate_Edit(t *testing.T) {
	m, ts := setup(t)
	out := mustCall(t, ts, "create_goal", `{"objective":"old","max_rounds":0}`, hostCtx(true))
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(out), &created)
	mustCall(t, ts, "update_goal", `{"id":"`+created.ID+`","action":"edit","reason":"","objective":"new objective"}`, hostCtx(true))
	g, _ := m.Get(context.Background(), created.ID)
	if g.Objective != "new objective" {
		t.Fatalf("objective = %q, want new objective", g.Objective)
	}
}

func TestUpdate_UnknownAction(t *testing.T) {
	_, ts := setup(t)
	if _, err := call(t, ts, "update_goal", `{"id":"g","action":"dance","reason":""}`, hostCtx(true)); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("unknown action = %v, want error", err)
	}
}
