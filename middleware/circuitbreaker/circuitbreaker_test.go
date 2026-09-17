package circuitbreaker

import (
	"sync"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
)

func TestCircuitBreaker_Constructor(t *testing.T) {
	cb := New("test", nil)
	if cb.Name() != "test" {
		t.Errorf("Name = %q, want 'test'", cb.Name())
	}
	if cb.State() != kernel.CircuitClosed {
		t.Errorf("initial state = %v, want closed", cb.State())
	}
}

func TestCircuitBreaker_DefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxFailures != 5 {
		t.Errorf("MaxFailures = %d, want 5", cfg.MaxFailures)
	}
	if cfg.ResetTimeout <= 0 {
		t.Error("ResetTimeout should be > 0")
	}
}

func TestCircuitBreaker_Closed_Allows(t *testing.T) {
	cb := New("test", &Config{MaxFailures: 2, ResetTimeout: time.Minute})
	if !cb.Allow() {
		t.Error("closed state should allow")
	}
}

func TestCircuitBreaker_FailureToOpen(t *testing.T) {
	cb := New("test", &Config{MaxFailures: 2, ResetTimeout: time.Minute})
	cb.RecordFailure()
	if cb.State() != kernel.CircuitClosed {
		t.Errorf("1 failure: state = %v, want closed", cb.State())
	}
	cb.RecordFailure()
	if cb.State() != kernel.CircuitOpen {
		t.Errorf("2 failures: state = %v, want open", cb.State())
	}
}

func TestCircuitBreaker_Open_Rejects(t *testing.T) {
	cb := New("test", &Config{MaxFailures: 1, ResetTimeout: time.Minute})
	cb.RecordFailure()
	if cb.Allow() {
		t.Error("open state should reject")
	}
}

func TestCircuitBreaker_SuccessCloses(t *testing.T) {
	cb := New("test", &Config{MaxFailures: 1, HalfOpenMaxCalls: 1, ResetTimeout: 1})
	cb.RecordFailure()
	time.Sleep(time.Millisecond)
	if !cb.Allow() {
		t.Fatal("after small sleep, allow should transition to half-open")
	}
	if cb.State() != kernel.CircuitHalfOpen {
		t.Errorf("after allow: state = %v, want half-open", cb.State())
	}
	cb.RecordSuccess()
	if cb.State() != kernel.CircuitClosed {
		t.Errorf("after success: state = %v, want closed", cb.State())
	}
}

func TestCircuitBreaker_HalfOpen_RejectsWhenFull(t *testing.T) {
	cb := New("test", &Config{MaxFailures: 1, HalfOpenMaxCalls: 1, ResetTimeout: 1})
	cb.RecordFailure()
	time.Sleep(time.Millisecond)
	cb.Allow()
	cb.Allow()
	if cb.Allow() {
		t.Error("half-open with no remaining slots should reject")
	}
}

func TestCircuitBreaker_FailureInHalfOpen_GoesBackToOpen(t *testing.T) {
	cb := New("test", &Config{MaxFailures: 1, HalfOpenMaxCalls: 1, ResetTimeout: 1})
	cb.RecordFailure()
	time.Sleep(time.Millisecond)
	cb.Allow()
	cb.Allow()
	cb.RecordFailure()
	if cb.State() != kernel.CircuitOpen {
		t.Errorf("failure in half-open: state = %v, want open", cb.State())
	}
}

func TestCircuitBreaker_Concurrency(t *testing.T) {
	cb := New("concurrent-test", &Config{
		MaxFailures:      5,
		HalfOpenMaxCalls: 3,
		ResetTimeout:     time.Minute,
	})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cb.Allow()
				cb.RecordSuccess()
			}
		}()
	}
	wg.Wait()
}

func TestCircuitBreaker_Interface(t *testing.T) {
	var _ kernel.CircuitBreaker = New("test", nil)
}
