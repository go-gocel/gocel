package registry

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRegistry_RegisterResolveUnregister(t *testing.T) {
	r := New[string](ScopeSession)
	if err := r.Register("a", "1"); err != nil {
		t.Fatalf("Register(a) = %v, want nil", err)
	}
	if err := r.Register("a", "2"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate Register(a) = %v, want ErrNameTaken", err)
	}
	if v, ok := r.Get("a"); !ok || v != "1" {
		t.Fatalf("Get(a) = %q, %v; want 1, true", v, ok)
	}
	if v, ok := r.Get("missing"); ok || v != "" {
		t.Fatalf("Get(missing) = %q, %v; want zero, false", v, ok)
	}
	if !r.Unregister("a") {
		t.Fatal("Unregister(a) = false, want true")
	}
	if r.Unregister("a") {
		t.Fatal("second Unregister(a) = true, want false")
	}
	if _, ok := r.Get("a"); ok {
		t.Fatal("Get(a) after Unregister = true, want false")
	}
}

func TestRegistry_EmptyNameRejected(t *testing.T) {
	r := New[int](ScopeGlobal)
	if err := r.Register("", 1); err == nil {
		t.Fatal("Register(\"\") = nil, want error")
	}
}

func TestRegistry_ObserveOwnRegistrations(t *testing.T) {
	r := New[int](ScopeHost)
	var mu sync.Mutex
	var got []Entry[int]
	cancel := r.Observe(func(e Entry[int]) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	_ = r.Register("x", 1)
	_ = r.Register("y", 2)
	cancel()
	_ = r.Register("z", 3)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Name != "x" || got[0].Scope != ScopeHost || got[1].Name != "y" {
		t.Fatalf("observed = %+v, want [x y] at host scope", got)
	}
}

func TestRegistry_EventsBubbleUpOnly(t *testing.T) {
	glob := New[string](ScopeGlobal)
	host := New[string](ScopeHost)
	sess := New[string](ScopeSession)
	if err := host.LinkParent(glob); err != nil {
		t.Fatal(err)
	}
	if err := sess.LinkParent(host); err != nil {
		t.Fatal(err)
	}

	var globalSeen, hostSeen, sessionSeen []string
	glob.Observe(func(e Entry[string]) { globalSeen = append(globalSeen, e.Name) })
	host.Observe(func(e Entry[string]) { hostSeen = append(hostSeen, e.Name) })
	sess.Observe(func(e Entry[string]) { sessionSeen = append(sessionSeen, e.Name) })

	// A session registration notifies the session, then host, then global
	// (upward only).
	_ = sess.Register("s1", "v")
	if len(sessionSeen) != 1 || sessionSeen[0] != "s1" {
		t.Fatalf("session observers = %v, want [s1]", sessionSeen)
	}
	if len(hostSeen) != 1 || hostSeen[0] != "s1" {
		t.Fatalf("host observers = %v, want [s1]", hostSeen)
	}
	if len(globalSeen) != 1 || globalSeen[0] != "s1" {
		t.Fatalf("global observers = %v, want [s1]", globalSeen)
	}

	// A global registration must never reach the session (events never flow
	// downward).
	_ = glob.Register("g1", "v")
	if len(sessionSeen) != 1 {
		t.Fatalf("session observers after global register = %v, want no new events", sessionSeen)
	}
	if len(hostSeen) != 1 {
		t.Fatalf("host observers after global register = %v, want no new events", hostSeen)
	}
}

func TestRegistry_LinkParentValidation(t *testing.T) {
	// Same scope is not strictly narrower.
	a := New[int](ScopeHost)
	b := New[int](ScopeHost)
	if err := a.LinkParent(b); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("LinkParent(same scope) = %v, want ErrInvalidParent", err)
	}

	// Reversed direction (child narrower than parent) is invalid.
	host := New[int](ScopeHost)
	sess := New[int](ScopeSession)
	if err := host.LinkParent(sess); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("LinkParent(reversed) = %v, want ErrInvalidParent", err)
	}

	// Second bind fails: a scope binds its parent exactly once.
	c1 := New[int](ScopeHost)
	c2 := New[int](ScopeGlobal)
	if err := c1.LinkParent(c2); err != nil {
		t.Fatal(err)
	}
	if err := c1.LinkParent(New[int](ScopeGlobal)); !errors.Is(err, ErrParentBound) {
		t.Fatalf("second LinkParent = %v, want ErrParentBound", err)
	}

	// Cycles are rejected.
	g := New[int](ScopeGlobal)
	h := New[int](ScopeHost)
	_ = h.LinkParent(g)
	if err := g.LinkParent(h); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("cycle LinkParent = %v, want ErrInvalidParent", err)
	}

	// Nil parent rejected.
	if err := h.LinkParent(nil); err == nil {
		t.Fatal("LinkParent(nil) = nil, want error")
	}
}

func TestView_NearestScopeWins(t *testing.T) {
	sess := New[string](ScopeSession)
	host := New[string](ScopeHost)
	glob := New[string](ScopeGlobal)

	_ = glob.Register("shared", "global")
	_ = host.Register("host-only", "host")
	_ = sess.Register("shared", "session") // shadows the global entry

	v := NewView(sess, host, glob)

	if e, ok := v.Resolve("shared"); !ok || e.Value != "session" || e.Scope != ScopeSession {
		t.Fatalf("Resolve(shared) = %+v, %v; want session entry", e, ok)
	}
	if e, ok := v.Resolve("host-only"); !ok || e.Value != "host" || e.Scope != ScopeHost {
		t.Fatalf("Resolve(host-only) = %+v, %v; want host entry", e, ok)
	}
	if _, ok := v.Resolve("missing"); ok {
		t.Fatal("Resolve(missing) = true, want false")
	}
}

func TestView_ListDeduplicates(t *testing.T) {
	sess := New[string](ScopeSession)
	glob := New[string](ScopeGlobal)
	_ = glob.Register("a", "global-a")
	_ = glob.Register("b", "global-b")
	_ = sess.Register("a", "session-a")

	entries := NewView(sess, glob).List()
	if len(entries) != 2 {
		t.Fatalf("List() len = %d, want 2", len(entries))
	}
	if entries[0].Name != "a" || entries[0].Value != "session-a" {
		t.Fatalf("List()[0] = %+v, want session-a", entries[0])
	}
	if entries[1].Name != "b" || entries[1].Value != "global-b" {
		t.Fatalf("List()[1] = %+v, want global-b", entries[1])
	}
}

func TestView_SnapshotIsACopy(t *testing.T) {
	glob := New[int](ScopeGlobal)
	_ = glob.Register("n", 7)
	snap := NewView(glob).Snapshot()
	snap["n"] = 99
	if v, _ := glob.Get("n"); v != 7 {
		t.Fatalf("registry value changed to %d via snapshot mutation", v)
	}
}

func TestRegistry_ConcurrentUse(t *testing.T) {
	r := New[int](ScopeSession)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = r.Register(string(rune('a'+n%26))+string(rune('0'+n/26)), n)
			_, _ = r.Get("a0")
			_ = r.Names()
		}(i)
	}
	wg.Wait()
	if len(r.Names()) != 32 {
		t.Fatalf("Names() len = %d, want 32", len(r.Names()))
	}
}

// TestRegistry_ObserveRemovals: removal observers fire on Unregister, at
// the registry itself and (upward only) at linked ancestors.
func TestRegistry_ObserveRemovals(t *testing.T) {
	glob := New[string](ScopeGlobal)
	host := New[string](ScopeHost)
	if err := host.LinkParent(glob); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var hostRemovals, globalRemovals []Entry[string]
	host.ObserveRemovals(func(e Entry[string]) {
		mu.Lock()
		hostRemovals = append(hostRemovals, e)
		mu.Unlock()
	})
	glob.ObserveRemovals(func(e Entry[string]) {
		mu.Lock()
		globalRemovals = append(globalRemovals, e)
		mu.Unlock()
	})

	_ = host.Register("x", "v")
	if !host.Unregister("x") {
		t.Fatal("Unregister(x) = false, want true")
	}
	// Global observers see the host removal (upward), host observers see it
	// directly; a global-only removal never reaches the host (downward).
	_ = glob.Register("g", "v")
	_ = glob.Unregister("g")

	mu.Lock()
	defer mu.Unlock()
	if len(hostRemovals) != 1 || hostRemovals[0].Name != "x" || hostRemovals[0].Scope != ScopeHost {
		t.Fatalf("host removals = %+v, want [x at host]", hostRemovals)
	}
	if len(globalRemovals) != 2 || globalRemovals[0].Name != "x" || globalRemovals[1].Name != "g" {
		t.Fatalf("global removals = %+v, want [x g]", globalRemovals)
	}
}

// TestView_ObserveChanges_NetVisibleDeltas: the change feed reports names
// that appear in or disappear from the view snapshot after shadowing, and
// never replays the initial snapshot.
func TestView_ObserveChanges_NetVisibleDeltas(t *testing.T) {
	sess := New[string](ScopeSession)
	glob := New[string](ScopeGlobal)
	_ = glob.Register("pre-existing", "global") // exists before the view
	v := NewView(sess, glob)

	var mu sync.Mutex
	var changes []Change[string]
	cancel := v.ObserveChanges(func(c Change[string]) {
		mu.Lock()
		changes = append(changes, c)
		mu.Unlock()
	})
	defer cancel()

	// Register at the session layer: becomes visible → addition.
	_ = sess.Register("new", "session")
	// Register the same name globally: shadowed, no net change.
	_ = glob.Register("new", "global")
	// Register a distinct global: becomes visible → addition.
	_ = glob.Register("other", "global")
	// Remove the session entry: "new" is still visible via global → no net
	// change for "new" (the feed reports removals only when visibility
	// flips).
	_ = sess.Unregister("new")
	// Remove the global "other": visibility flips → removal.
	_ = glob.Unregister("other")

	mu.Lock()
	defer mu.Unlock()
	var adds, removes []string
	for _, c := range changes {
		if c.Removed {
			removes = append(removes, c.Name)
		} else {
			adds = append(adds, c.Name)
		}
	}
	if len(adds) != 2 || adds[0] != "new" || adds[1] != "other" {
		t.Fatalf("additions = %v, want [new other]", adds)
	}
	if len(removes) != 1 || removes[0] != "other" {
		t.Fatalf("removals = %v, want [other]", removes)
	}
}

// TestView_ObserveChanges_Cancel: cancelling stops the feed.
func TestView_ObserveChanges_Cancel(t *testing.T) {
	r := New[int](ScopeGlobal)
	v := NewView(r)
	n := 0
	cancel := v.ObserveChanges(func(Change[int]) { n++ })
	_ = r.Register("a", 1)
	cancel()
	_ = r.Register("b", 2)
	time.Sleep(20 * time.Millisecond)
	if n != 1 {
		t.Fatalf("changes after cancel = %d, want 1", n)
	}
}
