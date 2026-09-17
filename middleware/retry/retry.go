// Package retry provides a gocel Middleware that retries failed model calls.
// The policy mirrors the semantics DSH's llm-retry validated in production:
//
//   - normal mode: a finite retry budget over retryable failures with
//     bounded exponential backoff and symmetric jitter.
//   - always mode: retry every model-request failure without an attempt
//     limit; success, cancellation, or middleware disposal stops it.
//   - A provider-suggested wait (kernel.RateLimitError.RetryAfter) replaces
//     the local backoff for that attempt when it is within the configured
//     maximum — the provider's instruction wins, without jitter.
//   - The caller's cancellation is never retried; non-retryable failures
//     (circuit breaks, input/not-found) return immediately.
//
// The jitter (default ±10%) keeps synchronized retry storms from stacking
// at the same instant — the failure mode DSH's bounded exponential backoff
// with jitter exists to avoid.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Mode selects the retry policy.
//
// Mode 选择重试策略。
type Mode int

const (
	// ModeNormal retries retryable failures up to MaxRetries with bounded
	// exponential backoff and jitter.
	//
	// ModeNormal 对可重试失败重试至多 MaxRetries 次，带界指数退避与抖动。
	ModeNormal Mode = iota
	// ModeAlways retries every model-request failure without an attempt
	// limit, until success, cancellation, or middleware disposal.
	//
	// ModeAlways 对每次模型请求失败无限重试，直到成功、取消或中间件
	// 销毁。
	ModeAlways
)

// Middleware retries failed model calls.
//
// Middleware 重试失败的模型调用。
type Middleware struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Mode       Mode
	// JitterRatio is the symmetric jitter fraction applied to every backoff
	// delay (0..1; 0.1 = ±10%). Zero uses the default 0.1.
	JitterRatio float64
}

// Option configures a Middleware.
//
// Option 配置 Middleware。
type Option func(*Middleware)

// WithMode selects the retry mode (default ModeNormal).
//
// WithMode 选择重试模式（默认 ModeNormal）。
func WithMode(m Mode) Option { return func(mw *Middleware) { mw.Mode = m } }

// WithJitterRatio sets the symmetric jitter fraction (default 0.1).
//
// WithJitterRatio 设置对称抖动比例（默认 0.1）。
func WithJitterRatio(r float64) Option { return func(mw *Middleware) { mw.JitterRatio = r } }

// New creates a retry middleware with the default normal mode.
//
// New 以默认 normal 模式创建重试中间件。
func New(maxRetries int, baseDelay, maxDelay time.Duration) *Middleware {
	return NewWithOptions(
		func(m *Middleware) {
			if maxRetries > 0 {
				m.MaxRetries = maxRetries
			}
			if baseDelay > 0 {
				m.BaseDelay = baseDelay
			}
			if maxDelay > 0 {
				m.MaxDelay = maxDelay
			}
		},
	)
}

// NewWithOptions creates a retry middleware with explicit options; missing
// values fall back to the defaults (3 retries, 1s base, 30s max, normal
// mode, ±10% jitter).
//
// NewWithOptions 以显式选项创建重试中间件；缺省值回退默认（3 次、
// 1s 基础、30s 上限、normal 模式、±10% 抖动）。
func NewWithOptions(opts ...Option) *Middleware {
	m := &Middleware{
		MaxRetries:  3,
		BaseDelay:   time.Second,
		MaxDelay:    30 * time.Second,
		Mode:        ModeNormal,
		JitterRatio: 0.1,
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.JitterRatio < 0 {
		m.JitterRatio = 0
	}
	return m
}

// Name returns the middleware name.
//
// Name 返回中间件名称。
func (m *Middleware) Name() string { return "retry" }

// WrapGenerate wraps a model handler with retry logic that retries only
// retryable failures.
//
// WrapGenerate 为模型处理器包装重试逻辑，仅重试可重试的失败。
func (m *Middleware) WrapGenerate(next kernel.ModelHandler) kernel.ModelHandler {
	return func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		var lastErr error
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				delay := m.delayFor(attempt, lastErr)
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-time.After(delay):
				}
			}
			resp, usage, err := next(ctx, msgs)
			if err == nil {
				return resp, usage, nil
			}
			lastErr = err
			if !isRetryable(err) {
				return nil, nil, err
			}
			if m.Mode == ModeNormal && attempt >= m.MaxRetries {
				return nil, nil, fmt.Errorf("retry: %d attempts: %w", m.MaxRetries+1, lastErr)
			}
		}
	}
}

// WrapStream wraps a streaming handler with retry logic that retries only
// retryable stream-open failures.
//
// WrapStream 为流式处理器包装重试逻辑，仅重试可重试的流开启失败。
func (m *Middleware) WrapStream(next kernel.StreamHandler) kernel.StreamHandler {
	return func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		var lastErr error
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				delay := m.delayFor(attempt, lastErr)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
			}
			reader, err := next(ctx, msgs)
			if err == nil {
				return reader, nil
			}
			lastErr = err
			if !isRetryable(err) {
				return nil, err
			}
			if m.Mode == ModeNormal && attempt >= m.MaxRetries {
				return nil, fmt.Errorf("retry stream: %d attempts: %w", m.MaxRetries+1, lastErr)
			}
		}
	}
}

// delayFor computes the backoff for the next attempt. A provider-suggested
// wait (RateLimitError.RetryAfter) replaces the local backoff when it is
// positive and within the configured maximum — the provider's instruction
// wins, without jitter (DSH providerRetryAfterMs semantics). Otherwise the
// bounded exponential backoff applies with symmetric jitter.
func (m *Middleware) delayFor(attempt int, lastErr error) time.Duration {
	if wait := providerRetryAfter(lastErr); wait > 0 {
		if wait > m.MaxDelay {
			wait = m.MaxDelay
		}
		return wait
	}
	delay := m.backoff(attempt)
	if m.JitterRatio > 0 {
		delay = jitter(delay, m.JitterRatio)
	}
	return delay
}

// providerRetryAfter extracts the provider-suggested wait (seconds) from a
// RateLimitError, or 0 when absent.
func providerRetryAfter(err error) time.Duration {
	var re *kernel.RateLimitError
	if errors.As(err, &re) && re.RetryAfter > 0 {
		return time.Duration(re.RetryAfter) * time.Second
	}
	return 0
}

func (m *Middleware) backoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	// Cap the shift so huge MaxRetries cannot overflow the shift into
	// a negative duration (which would skip the delay entirely).
	shift := attempt - 1
	if shift > 62 {
		shift = 62
	}
	delay := m.BaseDelay * (1 << shift)
	if delay > m.MaxDelay || delay <= 0 {
		delay = m.MaxDelay
	}
	return delay
}

// jitter applies symmetric jitter: the returned delay is within
// [d*(1-r), d*(1+r)] with uniform probability.
func jitter(d time.Duration, ratio float64) time.Duration {
	if d <= 0 {
		return 0
	}
	span := float64(d) * ratio
	delta := span * (2*rand.Float64() - 1)
	return d + time.Duration(delta)
}

// isRetryable delegates to the single retryability authority
// (kernel.IsRetryableError): providers classify transient failures as
// retryable ModelErrors; rate limits retry; circuit breaks and input/not
// found errors do not. The caller's cancellation is never retried. The
// old string-scanning allowlist is gone — error text is not a contract.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	// The caller decided to stop: never retry.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return kernel.IsRetryableError(err)
}
