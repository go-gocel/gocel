package projection

import (
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

// todoUnit is the reference domain: a todo list folded from todo/write
// events (whole-value last-wins, DSH todo projection).
type todoUnit struct {
	mu    sync.Mutex
	todos []string
}

func (u *todoUnit) Key() string { return "todos" }
func (u *todoUnit) Reset() {
	u.mu.Lock()
	u.todos = nil
	u.mu.Unlock()
}
func (u *todoUnit) Apply(ev types.SessionEvent) {
	if ev.Kind != "todo/write" {
		return
	}
	if items, ok := ev.Meta["items"].([]any); ok {
		u.mu.Lock()
		u.todos = nil
		for _, it := range items {
			if s, ok := it.(string); ok {
				u.todos = append(u.todos, s)
			}
		}
		u.mu.Unlock()
	}
}
func (u *todoUnit) View() any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.todos...)
}

func todoEvent(items ...string) types.SessionEvent {
	ev := types.NewLogOnlyEvent("todo/write", map[string]any{"items": toAnySlice(items)})
	return ev
}

func toAnySlice(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

// TestProjection_FoldsWholeValues: the unit folds log events into the
// derived read model; the snapshot serves the latest whole value.
func TestProjection_FoldsWholeValues(t *testing.T) {
	log := session.NewLog("s1")
	reg := New()
	if err := reg.Register(&todoUnit{}); err != nil {
		t.Fatal(err)
	}
	detach, err := reg.Attach(log)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()

	log.Append(todoEvent("a", "b"))
	snap := reg.Snapshot("s1")
	todos, ok := snap["todos"].([]string)
	if !ok || len(todos) != 2 || todos[0] != "a" || todos[1] != "b" {
		t.Fatalf("snapshot todos = %+v, want [a b]", snap["todos"])
	}

	// Whole-value last-wins: a later write replaces the list.
	log.Append(todoEvent("c"))
	snap = reg.Snapshot("s1")
	todos = snap["todos"].([]string)
	if len(todos) != 1 || todos[0] != "c" {
		t.Fatalf("after second write todos = %+v, want [c]", snap["todos"])
	}
}

// TestProjection_ChangeFeed: the feed reports only units whose view
// changed, per committed event, with the session id.
func TestProjection_ChangeFeed(t *testing.T) {
	log := session.NewLog("s1")
	reg := New()
	reg.Register(&todoUnit{})
	detach, _ := reg.Attach(log)
	defer detach()

	var mu sync.Mutex
	var calls []string
	cancel := reg.OnChanged(func(key, sessionID string) {
		mu.Lock()
		calls = append(calls, key+":"+sessionID)
		mu.Unlock()
	})
	defer cancel()

	log.Append(todoEvent("a"))                       // todo view changes → notify
	log.Append(types.NewLogOnlyEvent("other", nil))  // unrelated → no notify
	log.Append(todoEvent("a"))                       // same view → no notify
	log.Append(todoEvent("b"))                       // view changes → notify

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "todos:s1" || calls[1] != "todos:s1" {
		t.Fatalf("feed = %v, want two todos:s1 calls", calls)
	}
}

// TestProjection_UnregisterStopsFeeding: unregistering a unit removes it
// from the feed — later commits stop folding into it, and a re-registered
// unit of the same key folds from its registration moment (replaying the
// attached log's history).
func TestProjection_UnregisterStopsFeeding(t *testing.T) {
	log := session.NewLog("s1")
	reg := New()
	u := &todoUnit{}
	if err := reg.Register(u); err != nil {
		t.Fatal(err)
	}
	detach, _ := reg.Attach(log)
	defer detach()

	log.Append(todoEvent("a"))
	snap := reg.Snapshot("s1")
	if todos, _ := snap["todos"].([]string); len(todos) != 1 {
		t.Fatalf("before unregister todos = %+v, want [a]", snap["todos"])
	}

	if !reg.Unregister("todos") {
		t.Fatal("Unregister must report the unit existed")
	}
	log.Append(todoEvent("b"))
	if got := reg.Snapshot("s1"); len(got) != 0 {
		t.Fatalf("after unregister snapshot = %+v, want empty", got)
	}

	// Re-register: the unit replays the attached log (last-wins whole
	// value → the latest [b]) and resumes feeding.
	if err := reg.Register(&todoUnit{}); err != nil {
		t.Fatal(err)
	}
	snap = reg.Snapshot("s1")
	if todos, _ := snap["todos"].([]string); len(todos) != 1 || todos[0] != "b" {
		t.Fatalf("after re-register todos = %+v, want [b] (replayed last-wins)", snap["todos"])
	}
	// Unregistering an unknown key reports false.
	if reg.Unregister("ghost") {
		t.Fatal("Unregister of an unknown key must report false")
	}
}

// TestProjection_RegisterAfterEventsConverges: a unit registered after
// events flowed replays the attached log and converges.
func TestProjection_RegisterAfterEventsConverges(t *testing.T) {
	log := session.NewLog("s1")
	log.Append(todoEvent("early"))
	reg := New()
	detach, _ := reg.Attach(log)
	defer detach()

	// Register AFTER the event: the unit must fold the committed history.
	u := &todoUnit{}
	if err := reg.Register(u); err != nil {
		t.Fatal(err)
	}
	snap := reg.Snapshot("s1")
	todos := snap["todos"].([]string)
	if len(todos) != 1 || todos[0] != "early" {
		t.Fatalf("late-registered unit = %+v, want [early]", snap["todos"])
	}
}

// TestProjection_DuplicateKeyRejected: duplicate unit keys fail loudly.
func TestProjection_DuplicateKeyRejected(t *testing.T) {
	reg := New()
	if err := reg.Register(&todoUnit{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&todoUnit{}); err == nil {
		t.Fatal("duplicate unit key must be rejected")
	}
	if err := reg.Register(nil); err == nil {
		t.Fatal("nil unit must be rejected")
	}
}

// TestProjection_DetachStopsTheFeed: detaching stops driving events.
func TestProjection_DetachStopsTheFeed(t *testing.T) {
	log := session.NewLog("s1")
	reg := New()
	reg.Register(&todoUnit{})
	detach, _ := reg.Attach(log)

	log.Append(todoEvent("a"))
	detach()
	log.Append(todoEvent("b"))
	snap := reg.Snapshot("s1")
	todos := snap["todos"].([]string)
	if len(todos) != 1 || todos[0] != "a" {
		t.Fatalf("after detach the unit must freeze, got %+v", snap["todos"])
	}
}
