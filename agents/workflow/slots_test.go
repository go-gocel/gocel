package workflow

import (
	"context"
	"testing"
	"time"
)

// TestSlotQueue_AcquireWhenFree proves the fast path: free slots are taken
// without queuing.
func TestSlotQueue_AcquireWhenFree(t *testing.T) {
	q := newSlotQueue(2)
	if !q.acquire(context.Background()) {
		t.Fatal("first acquire must succeed")
	}
	if !q.acquire(context.Background()) {
		t.Fatal("second acquire must succeed")
	}
	// A third acquire must block; verify via ctx timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if q.acquire(ctx) {
		t.Fatal("acquire over capacity must block")
	}
	if ctx.Err() == nil {
		t.Fatal("acquire over capacity must respect ctx")
	}
}

// TestSlotQueue_StrictFIFOHandoff locks the DSH acquireSlot contract:
// release hands the slot directly to the OLDEST queued waiter, in order.
func TestSlotQueue_StrictFIFOHandoff(t *testing.T) {
	q := newSlotQueue(1)
	ctx := context.Background()
	if !q.acquire(ctx) {
		t.Fatal("initial acquire must succeed")
	}

	// Three waiters queue in arrival order (direct queue inspection keeps
	// the test deterministic).
	ch0, ch1, ch2 := make(chan struct{}), make(chan struct{}), make(chan struct{})
	q.mu.Lock()
	q.waiters = []chan struct{}{ch0, ch1, ch2}
	q.mu.Unlock()

	q.release() // slot -> oldest waiter
	select {
	case <-ch0:
	case <-time.After(5 * time.Second):
		t.Fatal("oldest waiter was not released first")
	}
	q.release()
	select {
	case <-ch1:
	case <-time.After(5 * time.Second):
		t.Fatal("second waiter was not released next")
	}
	q.release()
	select {
	case <-ch2:
	case <-time.After(5 * time.Second):
		t.Fatal("third waiter was not released last")
	}
}

// TestSlotQueue_CanceledWaiterLeavesQueue proves a canceled waiter removes
// itself without losing the place of the waiters behind it.
func TestSlotQueue_CanceledWaiterLeavesQueue(t *testing.T) {
	q := newSlotQueue(1)
	if !q.acquire(context.Background()) {
		t.Fatal("initial acquire must succeed")
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		q.acquire(ctxA)
	}()
	time.Sleep(50 * time.Millisecond) // let A enter the queue
	cancelA()
	select {
	case <-aDone:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiter must return")
	}

	// B queues behind A's (now removed) place and must receive the slot.
	doneB := make(chan struct{})
	go func() {
		q.acquire(context.Background())
		close(doneB)
	}()
	time.Sleep(50 * time.Millisecond)

	q.release()
	select {
	case <-doneB:
	case <-time.After(5 * time.Second):
		t.Fatal("the surviving waiter must receive the released slot")
	}
}
