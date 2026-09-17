// Package circuitbreaker provides the concrete CircuitBreaker implementation.
//
// The CircuitBreaker interface and the kernel.Middleware adapter are defined
// in github.com/go-gocel/gocel/core/kernel. This package provides the default
// implementation: a stateful circuit breaker with closed/open/half-open states.
//
// circuitbreaker 包提供熔断器的具体实现。
// CircuitBreaker 接口和 kernel.Middleware 适配器定义在 gocel/core/kernel 中。
// 本包提供默认实现：带三种状态的熔断器。
package circuitbreaker

import (
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
)

// Config configures a circuit breaker.
//
// Config 配置熔断器。
type Config struct {
	MaxFailures      int
	ResetTimeout     time.Duration
	HalfOpenMaxCalls int
}

// DefaultConfig returns the default circuit breaker configuration.
//
// DefaultConfig 返回默认熔断器配置。
func DefaultConfig() *Config {
	return &Config{
		MaxFailures:      5,
		ResetTimeout:     30 * time.Second,
		HalfOpenMaxCalls: 3,
	}
}

// Breaker implements kernel.CircuitBreaker with closed/open/half-open states.
//
// Breaker 以 closed/open/half-open 三种状态实现 kernel.CircuitBreaker。
type Breaker struct {
	name        string
	maxFail     int
	resetAfter  time.Duration
	halfOpenMax int

	mu            sync.RWMutex
	state         kernel.CircuitState
	failures      int
	lastFailure   time.Time
	halfOpenCalls int
}

// New creates a new Breaker. Invalid config values fall back to defaults
// so a misconfigured breaker degrades safely instead of breaking the
// state machine (e.g. MaxFailures=0 would open on the first failure,
// ResetTimeout=0 would defeat the open state entirely).
//
// New 创建一个新的 Breaker。无效配置值回退到默认值，使配置错误的熔断器
// 安全降级而不是破坏状态机（例如 MaxFailures=0 会在第一次失败时就打开，
// ResetTimeout=0 会完全破坏打开状态）。
func New(name string, cfg *Config) *Breaker {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = DefaultConfig().MaxFailures
	}
	if cfg.ResetTimeout <= 0 {
		cfg.ResetTimeout = DefaultConfig().ResetTimeout
	}
	if cfg.HalfOpenMaxCalls <= 0 {
		cfg.HalfOpenMaxCalls = DefaultConfig().HalfOpenMaxCalls
	}
	return &Breaker{
		name:        name,
		maxFail:     cfg.MaxFailures,
		resetAfter:  cfg.ResetTimeout,
		halfOpenMax: cfg.HalfOpenMaxCalls,
		state:       kernel.CircuitClosed,
	}
}

// Name returns the breaker's name.
//
// Name 返回熔断器名称。
func (cb *Breaker) Name() string { return cb.name }

// State returns the current circuit state.
//
// State 返回当前熔断状态。
func (cb *Breaker) State() kernel.CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Allow reports whether a call may proceed under the current state,
// transitioning open → half-open once the reset timeout has elapsed.
//
// Allow 判断当前状态下调用是否允许放行；打开状态超过重置时间后转入半开。
func (cb *Breaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case kernel.CircuitClosed:
		return true
	case kernel.CircuitOpen:
		if time.Since(cb.lastFailure) > cb.resetAfter {
			cb.state = kernel.CircuitHalfOpen
			cb.halfOpenCalls = 0
			return true
		}
		return false
	case kernel.CircuitHalfOpen:
		if cb.halfOpenCalls < cb.halfOpenMax {
			cb.halfOpenCalls++
			return true
		}
		return false
	}
	return false
}

// RecordSuccess records a successful call, resetting the failure count and
// closing a half-open circuit once its probe calls succeed.
//
// RecordSuccess 记录一次成功调用：清零失败计数；半开状态下探测调用成功
// 后闭合电路。
func (cb *Breaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case kernel.CircuitHalfOpen:
		cb.halfOpenCalls--
		if cb.halfOpenCalls <= 0 {
			cb.state = kernel.CircuitClosed
			cb.failures = 0
		}
	case kernel.CircuitClosed:
		cb.failures = 0
	}
}

// RecordFailure records a failed call, opening the circuit once the
// failure threshold is reached.
//
// RecordFailure 记录一次失败调用：达到失败阈值时打开电路。
func (cb *Breaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.lastFailure = time.Now()

	switch cb.state {
	case kernel.CircuitHalfOpen:
		cb.state = kernel.CircuitOpen
		cb.failures = cb.maxFail
	case kernel.CircuitClosed:
		cb.failures++
		if cb.failures >= cb.maxFail {
			cb.state = kernel.CircuitOpen
		}
	}
}

// NewMiddleware creates a kernel.Middleware that protects model calls with
// the given CircuitBreaker. Delegates to kernel.NewCircuitBreakerMiddleware.
//
// NewMiddleware 创建一个用给定熔断器保护模型调用的 kernel.Middleware，
// 委托给 kernel.NewCircuitBreakerMiddleware。
func NewMiddleware(cb kernel.CircuitBreaker) kernel.Middleware {
	return kernel.NewCircuitBreakerMiddleware(cb)
}
