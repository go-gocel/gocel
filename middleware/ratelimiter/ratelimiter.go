// Package ratelimiter provides the concrete RateLimiter implementation.
//
// The RateLimiter interface and the kernel.Middleware adapter are defined
// in github.com/go-gocel/gocel/core/kernel. This package provides the
// default token-bucket implementation.
//
// ratelimiter 包提供限流器的具体实现。
// RateLimiter 接口和 kernel.Middleware 适配器定义在 gocel/core/kernel 中。
// 本包提供默认的令牌桶实现。
package ratelimiter

import (
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
)

// Limiter implements kernel.RateLimiter with token bucket algorithm.
//
// Limiter 以令牌桶算法实现 kernel.RateLimiter。
type Limiter struct {
	tokens   float64
	capacity float64
	rate     float64
	lastTime time.Time
	mu       sync.Mutex
}

// New creates a new Limiter.
// capacity is the maximum number of tokens (burst size).
// rate is the number of tokens added per second.
//
// New 创建一个新的 Limiter。
// capacity 是令牌上限（突发大小）；rate 是每秒补充的令牌数。
func New(capacity, rate float64) *Limiter {
	if capacity <= 0 {
		capacity = 1
	}
	if rate <= 0 {
		rate = 1
	}
	return &Limiter{
		tokens:   capacity,
		capacity: capacity,
		rate:     rate,
		lastTime: time.Now(),
	}
}

// Allow consumes one token when available and reports whether the call
// may proceed.
//
// Allow 在令牌可用时消耗一个令牌，并返回是否放行。
func (rl *Limiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastTime).Seconds()
	rl.lastTime = now

	rl.tokens += elapsed * rl.rate
	if rl.tokens > rl.capacity {
		rl.tokens = rl.capacity
	}

	if rl.tokens >= 1 {
		rl.tokens -= 1
		return true
	}
	return false
}

// NewMiddleware creates a kernel.Middleware that protects model calls with
// the given RateLimiter. Delegates to kernel.NewRateLimiterMiddleware.
//
// NewMiddleware 创建一个用给定限流器保护模型调用的 kernel.Middleware，
// 委托给 kernel.NewRateLimiterMiddleware。
func NewMiddleware(rl kernel.RateLimiter) kernel.Middleware {
	return kernel.NewRateLimiterMiddleware(rl)
}
