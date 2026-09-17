package runtime

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
)

// TestInMemoryState_WatcherReentrancy (B4): a watcher that mutates the
// state from inside its callback must not deadlock — callbacks run OUTSIDE
// the store's lock.
func TestInMemoryState_WatcherReentrancy(t *testing.T) {
	s := NewInMemoryState()
	var fired atomic.Int32
	s.Watch([]string{"a"}, func(changes []kernel.StateChange) {
		fired.Add(1)
		s.Set("b", "from-watcher") // re-entrant write
	})
	done := make(chan struct{})
	go func() {
		s.Set("a", 1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Set deadlocked: the watcher callback ran under the store lock")
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("watcher fired %d times, want 1", got)
	}
	if v, ok := s.Get("b"); !ok || v != "from-watcher" {
		t.Fatalf("re-entrant write lost: b=%v ok=%v", v, ok)
	}
}

// TestInMemoryState_EmptyKeysWatchAll (B4): an EMPTY non-nil key list
// means "watch everything", exactly like nil.
func TestInMemoryState_EmptyKeysWatchAll(t *testing.T) {
	s := NewInMemoryState()
	var fired atomic.Int32
	s.Watch([]string{}, func(changes []kernel.StateChange) {
		fired.Add(int32(len(changes)))
	})
	s.Set("any-key", 1)
	s.Set("other-key", 2)
	if got := fired.Load(); got != 2 {
		t.Fatalf("empty non-nil keys must watch all: fired %d, want 2", got)
	}
}

// TestInMemoryState_WithData prepopulates and isolates from the source map.
func TestInMemoryState_WithData(t *testing.T) {
	initial := map[string]any{"a": 1, "b": "two"}
	s := NewInMemoryStateWithData(initial)
	initial["c"] = 3 // must not leak in
	if len(s.Keys()) != 2 {
		t.Fatalf("Keys = %v, want the two initial keys", s.Keys())
	}
	if v, ok := s.Get("a"); !ok || v != 1 {
		t.Fatalf("a = %v ok=%v", v, ok)
	}
}
