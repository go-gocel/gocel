package hub

import (
	"sync"
	"testing"
)

type strEvent struct {
	streamID string
	seq      int64
}

func (e strEvent) StreamID() string { return e.streamID }
func (e strEvent) Seq() int64       { return e.seq }

// Regression: Publish vs Unsubscribe raced on the subscriber channel —
// Unsubscribe closed it while Publish sent outside the lock, panicking
// with "send on closed channel" under contention (C6, empirically
// reproduced). Delivery now happens under the hub lock.
func TestHub_PublishUnsubscribeRaceNoPanic(t *testing.T) {
	h := New[strEvent]()
	_, unsub := h.Subscribe()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.Publish(strEvent{streamID: "s", seq: 1})
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			_, un := h.Subscribe()
			un()
		}
		unsub()
		close(stop)
	}()

	wg.Wait()
}

// The hub stays functional after the storm.
func TestHub_AfterRaceStorm(t *testing.T) {
	h := New[strEvent]()
	ch, un := h.Subscribe()
	h.Publish(strEvent{streamID: "s", seq: 1})
	select {
	case ev := <-ch:
		if ev.Seq() != 1 {
			t.Fatalf("seq = %d, want 1", ev.Seq())
		}
	default:
		t.Fatal("event not delivered")
	}
	un()
	if h.Len() != 0 {
		t.Fatalf("subscribers left: %d", h.Len())
	}
}
