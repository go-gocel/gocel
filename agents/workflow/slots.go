package workflow

import (
	"context"
	"sync"
)

// slotQueue is a strict-FIFO semaphore (DSH acquireSlot semantics): waiters
// queue in arrival order and a release hands the slot directly to the
// OLDEST waiter. A channel semaphore wakes an arbitrary waiter — under
// load, item ordering is nondeterministic; the queue preserves it.
//
// slotQueue 是严格 FIFO 信号量（DSH acquireSlot 语义）：等待者按到达顺序
// 排队，release 把槽直接交给最早的等待者。channel 信号量唤醒的是任意
// 等待者——负载下条目顺序不确定；队列则保持顺序。
type slotQueue struct {
	mu      sync.Mutex
	free    int
	waiters []chan struct{}
}

func newSlotQueue(n int) *slotQueue {
	if n <= 0 {
		n = 1
	}
	return &slotQueue{free: n}
}

// acquire takes a slot, blocking until one frees or ctx is done. On
// cancellation the waiter leaves the queue without losing its place for
// others.
func (q *slotQueue) acquire(ctx context.Context) bool {
	q.mu.Lock()
	if q.free > 0 {
		q.free--
		q.mu.Unlock()
		return true
	}
	ch := make(chan struct{})
	q.waiters = append(q.waiters, ch)
	q.mu.Unlock()

	select {
	case <-ch:
		return true
	case <-ctx.Done():
		q.mu.Lock()
		removed := false
		for i, w := range q.waiters {
			if w == ch {
				q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
				removed = true
				break
			}
		}
		q.mu.Unlock()
		if !removed {
			// release() already popped and closed our channel: the slot
			// was handed to us — take it instead of losing it (the old
			// code dropped the slot permanently on this race).
			return true
		}
		return false
	}
}

// release frees one slot: the oldest waiter gets it directly, otherwise it
// returns to the free pool.
func (q *slotQueue) release() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.waiters) > 0 {
		w := q.waiters[0]
		q.waiters = q.waiters[1:]
		close(w)
		return
	}
	q.free++
}
