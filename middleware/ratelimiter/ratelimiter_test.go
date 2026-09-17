package ratelimiter

import (
	"sync"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
)

func TestRateLimiter_Constructor(t *testing.T) {
	rl := New(10, 5)
	if !rl.Allow() {
		t.Error("first call should be allowed")
	}
	if !rl.Allow() {
		t.Error("second call should be allowed (capacity 10)")
	}
}

func TestRateLimiter_BurstLimit(t *testing.T) {
	rl := New(5, 1) // capacity 5, refill 1/sec
	for i := 0; i < 5; i++ {
		if !rl.Allow() {
			t.Errorf("call %d should be allowed within burst", i+1)
		}
	}
	if rl.Allow() {
		t.Error("6th call should be rejected (burst exhausted)")
	}
}

func TestRateLimiter_Refill(t *testing.T) {
	rl := New(1, 10) // capacity 1, refill 10/sec
	if !rl.Allow() {
		t.Error("first call should be allowed")
	}
	if rl.Allow() {
		t.Error("second call should be rejected (capacity 1)")
	}
	time.Sleep(150 * time.Millisecond)
	if !rl.Allow() {
		t.Error("after wait, token should be refilled")
	}
}

func TestRateLimiter_Concurrency(t *testing.T) {
	rl := New(100, 1000)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				rl.Allow()
			}
		}()
	}
	wg.Wait()
}

func TestRateLimiter_Interface(t *testing.T) {
	var _ kernel.RateLimiter = New(10, 1)
}
