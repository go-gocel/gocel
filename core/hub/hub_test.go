package hub

import (
	"sync"
	"testing"
	"time"
)

// testEvent is a product-shaped event embedding the hub contract.
type testEvent struct {
	stream string
	seq    int64
	body   string
}

func (e testEvent) StreamID() string { return e.stream }
func (e testEvent) Seq() int64        { return e.seq }

// TestPublish_ReachesSubscribers: published events arrive at every
// subscriber in order.
func TestPublish_ReachesSubscribers(t *testing.T) {
	h := New[testEvent]()
	ch1, un1 := h.Subscribe()
	defer un1()
	ch2, un2 := h.Subscribe()
	defer un2()

	h.Publish(testEvent{stream: "s1", seq: 1, body: "a"})
	h.Publish(testEvent{stream: "s1", seq: 2, body: "b"})

	for i, ch := range []<-chan testEvent{ch1, ch2} {
		for want := int64(1); want <= 2; want++ {
			select {
			case ev := <-ch:
				if ev.Seq() != want {
					t.Fatalf("subscriber %d got seq %d, want %d", i, ev.Seq(), want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("subscriber %d missed seq %d", i, want)
			}
		}
	}
}

// TestReplay_CatchUp: a late subscriber replays the bounded buffer after
// its watermark and continues from the last seq.
func TestReplay_CatchUp(t *testing.T) {
	h := New[testEvent]()
	for i := int64(1); i <= 10; i++ {
		h.Publish(testEvent{stream: "s1", seq: i, body: "x"})
	}
	// A client that saw up to seq 7 catches up 8, 9, 10.
	got := h.Replay("s1", 7)
	if len(got) != 3 || got[0].Seq() != 8 || got[2].Seq() != 10 {
		t.Fatalf("replay after 7 = %+v, want seqs 8..10", got)
	}
	// Unknown stream yields nil.
	if h.Replay("ghost", 0) != nil {
		t.Fatal("unknown stream must yield nil")
	}
	// Stream-less events are never retained for replay.
	h.Publish(testEvent{seq: 1})
	if got := h.Replay("", 0); got != nil {
		t.Fatalf("stream-less events must not replay, got %+v", got)
	}
}

// TestReplay_Bounded: the per-stream buffer keeps only the newest events.
func TestReplay_Bounded(t *testing.T) {
	h := New[testEvent](WithReplayCap(3))
	for i := int64(1); i <= 10; i++ {
		h.Publish(testEvent{stream: "s1", seq: i})
	}
	got := h.Replay("s1", 0)
	if len(got) != 3 || got[0].Seq() != 8 || got[2].Seq() != 10 {
		t.Fatalf("bounded replay = %+v, want the newest 3 (seqs 8..10)", got)
	}
	// Forget releases the buffer.
	h.Forget("s1")
	if h.Replay("s1", 0) != nil {
		t.Fatal("after Forget the stream must replay nothing")
	}
}

// TestSlowSubscriber_DoesNotBlockProducer: a stalled subscriber drops its
// oldest events; the producer never blocks.
func TestSlowSubscriber_DoesNotBlockProducer(t *testing.T) {
	h := New[testEvent](WithSubscriberBuffer(4))
	ch, un := h.Subscribe()
	defer un()

	// Never drain ch; publish more than the buffer. Each publish must
	// return promptly.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(1); i <= 100; i++ {
			h.Publish(testEvent{stream: "s1", seq: i})
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("producer blocked on a slow subscriber")
	}
	// The subscriber still holds a bounded queue (drained below its cap).
	select {
	case ev := <-ch:
		if ev.Seq() == 0 {
			t.Fatal("bad event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber channel closed or empty")
	}
}

// TestUnsubscribe_StopsDelivery: unsubscribing stops delivery and closes
// the channel.
func TestUnsubscribe_StopsDelivery(t *testing.T) {
	h := New[testEvent]()
	ch, un := h.Subscribe()
	h.Publish(testEvent{stream: "s1", seq: 1})
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("initial event missing")
	}
	un()
	h.Publish(testEvent{stream: "s1", seq: 2})
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("delivery after unsubscribe")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after unsubscribe")
	}
}

// TestNegativeOptions_FallBackToDefaults: negative bounds are invalid
// input; the constructor clamps them to the defaults instead of panicking
// on make(chan T, -1) or a negative replay slice bound.
func TestNegativeOptions_FallBackToDefaults(t *testing.T) {
	h := New[testEvent](WithSubscriberBuffer(-1), WithReplayCap(-1))
	ch, un := h.Subscribe()
	defer un()
	h.Publish(testEvent{stream: "s1", seq: 1})
	h.Publish(testEvent{stream: "s1", seq: 2})
	if got := h.Replay("s1", 0); len(got) != 2 {
		t.Fatalf("replay with clamped cap = %d events, want 2", len(got))
	}
	select {
	case ev := <-ch:
		if ev.Seq() != 1 {
			t.Fatalf("first delivered seq = %d, want 1", ev.Seq())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("clamped subscriber channel never delivered")
	}
}

// TestConcurrentPublish: concurrent publishers never race the hub.
func TestConcurrentPublish(t *testing.T) {
	h := New[testEvent](WithSubscriberBuffer(2048))
	ch, un := h.Subscribe()
	defer un()

	// Drain concurrently so the fast consumer never drops.
	var wg sync.WaitGroup
	count := make(chan int, 1)
	go func() {
		n := 0
		for range ch {
			n++
		}
		count <- n
	}()

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := int64(1); i <= 200; i++ {
				h.Publish(testEvent{stream: "s1", seq: int64(g)*1000 + i})
			}
		}(g)
	}
	wg.Wait()
	// All publishes are complete (buffer 2048 > 1600 events, so nothing was
	// ever dropped). Unsubscribing closes the channel; the drainer consumes
	// the remainder and reports the exact count — fully deterministic.
	un()
	if got := <-count; got != 1600 {
		t.Fatalf("delivered %d, want 1600", got)
	}
}
