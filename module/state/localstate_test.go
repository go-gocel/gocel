package state

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLocalState_GetSet(t *testing.T) {
	ls := NewLocalState()
	ls.Set("name", "gocel")
	ls.Set("version", 2)

	v, ok := ls.Get("name")
	if !ok || v != "gocel" {
		t.Fatalf("expected 'gocel', got %v (ok=%v)", v, ok)
	}

	v, ok = ls.Get("version")
	if !ok || v != 2 {
		t.Fatalf("expected 2, got %v (ok=%v)", v, ok)
	}

	_, ok = ls.Get("nonexistent")
	if ok {
		t.Fatal("expected false for nonexistent key")
	}
}

func TestLocalState_Delete(t *testing.T) {
	ls := NewLocalState()
	ls.Set("key", "value")
	ls.Delete("key")

	_, ok := ls.Get("key")
	if ok {
		t.Fatal("expected key to be deleted")
	}
}

func TestLocalState_Keys(t *testing.T) {
	ls := NewLocalState()
	ls.Set("a", 1)
	ls.Set("b", 2)
	ls.Set("c", 3)

	keys := ls.Keys()
	sort.Strings(keys)

	if len(keys) != 3 || keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Fatalf("expected [a b c], got %v", keys)
	}
}

func TestLocalState_Snapshot(t *testing.T) {
	ls := NewLocalState()
	ls.Set("x", "hello")
	ls.Set("y", "world")

	snap := ls.Snapshot()
	if len(snap) != 2 || snap["x"] != "hello" || snap["y"] != "world" {
		t.Fatalf("unexpected snapshot: %v", snap)
	}

	// snapshot is isolated
	ls.Set("z", "new")
	if len(snap) != 2 {
		t.Fatal("snapshot should be isolated from subsequent writes")
	}
}

func TestLocalState_Clear(t *testing.T) {
	ls := NewLocalState()
	ls.Set("a", 1)
	ls.Set("b", 2)
	ls.Clear()

	if len(ls.Keys()) != 0 {
		t.Fatal("expected empty state after clear")
	}

	// clear doesn't break future writes
	ls.Set("c", 3)
	v, ok := ls.Get("c")
	if !ok || v != 3 {
		t.Fatal("expected to write after clear")
	}
}

func TestLocalState_NewWithData(t *testing.T) {
	initial := map[string]any{"a": 1, "b": "two"}
	ls := NewLocalStateWithData(initial)

	if len(ls.Keys()) != 2 {
		t.Fatal("expected 2 keys from initial data")
	}

	// mutation should not affect original map
	initial["c"] = 3
	if len(ls.Keys()) != 2 {
		t.Fatal("local state should not be affected by original map mutation")
	}
}

func TestLocalState_Watch(t *testing.T) {
	ls := NewLocalState()

	var mu sync.Mutex
	var received []StateChange
	cancel := ls.Watch([]string{"target"}, func(changes []StateChange) {
		mu.Lock()
		received = append(received, changes...)
		mu.Unlock()
	})

	ls.Set("target", "value1")
	ls.Set("other", "ignored")
	ls.Set("target", "value2")
	// Watcher callbacks run synchronously inside Set (outside the lock), so
	// the count is already final — no sleep needed.

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 2 {
		t.Fatalf("expected 2 changes for 'target', got %d", count)
	}

	// cancel and verify no more notifications
	cancel()
	ls.Set("target", "value3")

	mu.Lock()
	if len(received) != 2 {
		t.Fatal("expected no more changes after cancel")
	}
	mu.Unlock()
}

func TestLocalState_WatchAll(t *testing.T) {
	ls := NewLocalState()

	var count atomic.Int32
	ls.Watch(nil, func(changes []StateChange) {
		count.Add(int32(len(changes)))
	})

	ls.Set("a", 1)
	ls.Set("b", 2)
	ls.Delete("a")
	// Watcher callbacks run synchronously inside Set/Delete (outside the
	// lock) — the count is already final, no sleep needed.

	if c := count.Load(); c != 3 {
		t.Fatalf("expected 3 changes (nil keys = watch all), got %d", c)
	}
}

func TestLocalState_WatchNoMatch(t *testing.T) {
	ls := NewLocalState()

	var count atomic.Int32
	ls.Watch([]string{"monitor"}, func(changes []StateChange) {
		count.Add(int32(len(changes)))
	})

	ls.Set("other", "value")
	ls.Set("another", "value2")
	// Watcher callbacks run synchronously inside Set — final count, no sleep.

	if c := count.Load(); c != 0 {
		t.Fatalf("expected 0 changes (watcher for 'monitor' only), got %d", c)
	}
}

func TestLocalState_ConcurrentSafety(t *testing.T) {
	ls := NewLocalState()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "key_" + string(rune('A'+n))
			ls.Set(key, n)
			ls.Get(key)
			ls.Keys()
		}(i)
	}
	wg.Wait()

	// no data races — verified by -race
	if len(ls.Keys()) != 20 {
		t.Fatalf("expected 20 keys, got %d", len(ls.Keys()))
	}
}
