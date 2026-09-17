package kernel

import (
	"context"
	"errors"
	"io"

	"github.com/go-gocel/gocel/core/types"
)

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// CircuitBreaker is the interface for circuit breaker operations.
//
// CircuitBreaker 是熔断器操作接口。
type CircuitBreaker interface {
	Allow() bool
	RecordSuccess()
	RecordFailure()
	State() CircuitState
	Name() string
}

// CircuitState represents the state of a circuit breaker.
// CircuitState 表示熔断器的状态。
type CircuitState int

const (
	// CircuitClosed is the normal state where calls are allowed.
	// CircuitClosed 是正常状态，调用被允许。
	CircuitClosed CircuitState = iota
	// CircuitOpen is the tripped state where calls are rejected.
	// CircuitOpen 是熔断打开状态，调用被拒绝。
	CircuitOpen
	// CircuitHalfOpen is the probing state where a limited number of calls are allowed to test recovery.
	// CircuitHalfOpen 是半开探测状态，允许有限调用试探恢复。
	CircuitHalfOpen
)

// String returns the human-readable name of the circuit state.
// String 返回熔断状态的人类可读名称。
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// RateLimiter is the interface for rate limiting operations.
//
// RateLimiter 是限流器操作接口。
type RateLimiter interface {
	Allow() bool
}

// NewCircuitBreakerMiddleware creates a kernel.Middleware that protects model calls
// with the given CircuitBreaker. Both Generate and Stream calls are protected.
// NewCircuitBreakerMiddleware 创建用给定熔断器保护模型调用的中间件，Generate 与 Stream 调用均受保护。
func NewCircuitBreakerMiddleware(cb CircuitBreaker) Middleware {
	return NewFuncMiddleware(
		"circuit:"+cb.Name(),
		func(next ModelHandler) ModelHandler {
			return func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
				if !cb.Allow() {
					return nil, nil, NewCircuitError(cb.Name())
				}
				resp, usage, err := next(ctx, msgs)
				if err != nil {
					cb.RecordFailure()
					return resp, usage, err
				}
				cb.RecordSuccess()
				return resp, usage, nil
			}
		},
		func(next StreamHandler) StreamHandler {
			return func(ctx context.Context, msgs []*types.Message) (StreamReader, error) {
				if !cb.Allow() {
					return nil, NewCircuitError(cb.Name())
				}
				stream, err := next(ctx, msgs)
				if err != nil {
					cb.RecordFailure()
					return stream, err
				}
				return &circuitStreamReader{StreamReader: stream, cb: cb}, nil
			}
		},
	)
}

type circuitStreamReader struct {
	StreamReader
	cb     CircuitBreaker
	failed bool
}

// Recv reads the next chunk from the wrapped stream, recording failures
// (other than normal end-of-stream or cancellation) for the circuit breaker.
// Recv 从被包装的流中读取下一个 chunk；除正常结束（io.EOF）与取消外的
// 错误记为失败，供熔断器统计。
func (r *circuitStreamReader) Recv() (*types.Message, error) {
	msg, err := r.StreamReader.Recv()
	// io.EOF is the normal end-of-stream signal — it must not count as a
	// failure, otherwise every cleanly finished stream opens the breaker.
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		r.failed = true
	}
	return msg, err
}

// Close closes the underlying stream and reports the outcome (failure or
// success) to the circuit breaker.
// Close 关闭底层流，并将结果（失败或成功）上报给熔断器。
func (r *circuitStreamReader) Close() error {
	err := r.StreamReader.Close()
	if r.failed {
		r.cb.RecordFailure()
	} else {
		r.cb.RecordSuccess()
	}
	return err
}

// NewRateLimiterMiddleware creates a kernel.Middleware that protects model calls
// with the given RateLimiter. Both Generate and Stream calls are protected.
// NewRateLimiterMiddleware 创建用给定限流器保护模型调用的中间件，Generate 与 Stream 调用均受保护。
func NewRateLimiterMiddleware(rl RateLimiter) Middleware {
	return NewFuncMiddleware(
		"ratelimit",
		func(next ModelHandler) ModelHandler {
			return func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
				if !rl.Allow() {
					return nil, nil, ErrRateLimited
				}
				return next(ctx, msgs)
			}
		},
		func(next StreamHandler) StreamHandler {
			return func(ctx context.Context, msgs []*types.Message) (StreamReader, error) {
				if !rl.Allow() {
					return nil, ErrRateLimited
				}
				return next(ctx, msgs)
			}
		},
	)
}


