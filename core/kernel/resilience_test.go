package kernel

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/go-gocel/gocel/core/types"
)

// recordingBreaker records every call for assertions.
type recordingBreaker struct {
	allowCalls  int
	successes   int
	failures    int
	allowResult bool
}

func (b *recordingBreaker) Allow() bool {
	b.allowCalls++
	return b.allowResult
}
func (b *recordingBreaker) RecordSuccess() { b.successes++ }
func (b *recordingBreaker) RecordFailure() { b.failures++ }
func (b *recordingBreaker) State() CircuitState {
	return CircuitClosed
}
func (b *recordingBreaker) Name() string { return "recording" }

func TestCircuitBreakerMiddleware_GenerateSuccess(t *testing.T) {
	cb := &recordingBreaker{allowResult: true}
	mw := NewCircuitBreakerMiddleware(cb)

	resp := types.NewAssistantMessage("ok")
	handler := mw.WrapGenerate(func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return resp, nil, nil
	})

	out, _, err := handler(context.Background(), nil)
	if err != nil || out != resp {
		t.Fatalf("handler: out=%v err=%v", out, err)
	}
	if cb.allowCalls != 1 || cb.successes != 1 || cb.failures != 0 {
		t.Fatalf("breaker: allow=%d ok=%d fail=%d", cb.allowCalls, cb.successes, cb.failures)
	}
}

func TestCircuitBreakerMiddleware_GenerateFailure(t *testing.T) {
	cb := &recordingBreaker{allowResult: true}
	mw := NewCircuitBreakerMiddleware(cb)

	handler := mw.WrapGenerate(func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return nil, nil, errors.New("boom")
	})

	if _, _, err := handler(context.Background(), nil); err == nil {
		t.Fatal("expected error")
	}
	if cb.failures != 1 || cb.successes != 0 {
		t.Fatalf("breaker: ok=%d fail=%d", cb.successes, cb.failures)
	}
}

func TestCircuitBreakerMiddleware_GenerateRejected(t *testing.T) {
	cb := &recordingBreaker{allowResult: false}
	mw := NewCircuitBreakerMiddleware(cb)

	_, _, err := mw.WrapGenerate(func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		t.Fatal("next must not be called when the circuit is open")
		return nil, nil, nil
	})(context.Background(), nil)

	var ce *CircuitError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want CircuitError", err)
	}
	if ce.Route != "recording" {
		t.Fatalf("route = %q, want 'recording'", ce.Route)
	}
}

// TestCircuitStreamReader_EOFIsNotFailure is a regression test: a stream that
// ends normally (io.EOF) must count as a success — previously every cleanly
// finished stream opened the breaker.
func TestCircuitStreamReader_EOFIsNotFailure(t *testing.T) {
	cb := &recordingBreaker{allowResult: true}
	mw := NewCircuitBreakerMiddleware(cb)

	handler := mw.WrapStream(func(ctx context.Context, msgs []*types.Message) (StreamReader, error) {
		return &eofStreamReader{}, nil
	})

	reader, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	// Consume the stream to its natural end.
	for {
		_, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if cb.failures != 0 {
		t.Fatalf("clean EOF must not count as failure: %d", cb.failures)
	}
	if cb.successes != 1 {
		t.Fatalf("clean EOF should record a success: %d", cb.successes)
	}
}

// TestCircuitStreamReader_ErrorIsFailure: a stream that fails mid-way must
// count as a failure when closed.
func TestCircuitStreamReader_ErrorIsFailure(t *testing.T) {
	cb := &recordingBreaker{allowResult: true}
	mw := NewCircuitBreakerMiddleware(cb)

	handler := mw.WrapStream(func(ctx context.Context, msgs []*types.Message) (StreamReader, error) {
		return &failingStreamReader{}, nil
	})

	reader, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	for {
		_, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			break // stream failed mid-way
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if cb.failures != 1 {
		t.Fatalf("mid-stream error should count as failure: %d", cb.failures)
	}
	if cb.successes != 0 {
		t.Fatalf("mid-stream error must not record success: %d", cb.successes)
	}
}

type eofStreamReader struct{}

func (r *eofStreamReader) Recv() (*types.Message, error) { return nil, io.EOF }
func (r *eofStreamReader) Close() error                  { return nil }
func (r *eofStreamReader) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

type failingStreamReader struct {
	sent bool
}

func (r *failingStreamReader) Recv() (*types.Message, error) {
	if !r.sent {
		r.sent = true
		return types.NewAssistantMessage("partial"), nil
	}
	return nil, errors.New("stream broken")
}
func (r *failingStreamReader) Close() error { return nil }
func (r *failingStreamReader) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
