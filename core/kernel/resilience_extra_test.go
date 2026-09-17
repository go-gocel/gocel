package kernel_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Regression: circuitStreamReader compared cancellation by identity, so a
// wrapped context.Canceled was counted as a stream failure and tripped the
// breaker on a benign cancellation.

type fakeCB struct {
	failures  int
	successes int
}

func (f *fakeCB) Allow() bool                     { return true }
func (f *fakeCB) RecordSuccess()                  { f.successes++ }
func (f *fakeCB) RecordFailure()                  { f.failures++ }
func (f *fakeCB) State() kernel.CircuitState      { return kernel.CircuitClosed }
func (f *fakeCB) Name() string                    { return "fake" }

type errStream struct{ err error }

func (s *errStream) Recv() (*types.Message, error) { return nil, s.err }
func (s *errStream) Close() error                  { return nil }
func (s *errStream) Done() <-chan struct{}         { return nil }

func TestCircuitStreamReader_WrappedCancellationNotFailure(t *testing.T) {
	cb := &fakeCB{}
	mw := kernel.NewCircuitBreakerMiddleware(cb)
	handler := mw.WrapStream(func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		return &errStream{err: fmt.Errorf("stream: %w", context.Canceled)}, nil
	})
	sr, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("wrap stream: %v", err)
	}
	_, _ = sr.Recv()
	_ = sr.Close()

	if cb.failures != 0 {
		t.Fatalf("breaker recorded %d failures for a wrapped cancellation, want 0", cb.failures)
	}
	if cb.successes != 1 {
		t.Fatalf("breaker recorded %d successes, want 1", cb.successes)
	}
}
