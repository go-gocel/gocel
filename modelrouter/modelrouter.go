// Package modelrouter provides multi-model routing with circuit breaker, rate limiter, and middleware chain support.
//
// ModelRouter implements ChatModel and distributes calls across multiple model
// routes using configurable strategies (priority, round-robin, weighted, race).
// Each route can have its own circuit breaker and rate limiter. Route-level
// middleware chain can be applied via WithRouterMiddleware option.
//
// NOTE: For Runtime-level middleware (applied to ALL model calls regardless
// of router), use kernel.NewRuntime with kernel.WithMiddleware instead.
// The two levels of middleware layer naturally:
//
//	Runtime-level middleware (outermost, global)
//	  └── ModelRouter
//	        ├── route-level CircuitBreaker
//	        ├── route-level RateLimiter
//	        └── actual ChatModel
//
// 路由模块。ModelRouter 实现 ChatModel 接口，通过可配置的策略
// （优先级、轮询、权重、竞速）将调用分发给多个模型。
// 每个路由可以独立配置熔断器和限流器。路由级中间件链通过 WithRouterMiddleware 注入。
//
// 注意：Runtime 级中间件（通过 kernel.WithMiddleware 设置）对所有模型调用生效，
// 与路由级中间件自然叠加。
package modelrouter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ErrNoRoutes is returned when the router has no routes configured.
//
// ErrNoRoutes 在路由器未配置任何路由时返回。
var ErrNoRoutes = errors.New("model router: no routes configured")

// RouterStrategy defines how the ModelRouter selects among candidate models.
//
// RouterStrategy 定义 ModelRouter 如何在候选模型中进行选择。
type RouterStrategy string

const (
	// StrategyPriority tries models in registration order; falls through on error.
	//
	// StrategyPriority 按注册顺序依次尝试模型；出错时继续尝试下一个。
	StrategyPriority RouterStrategy = "priority"
	// StrategyRoundRobin distributes requests across all models in turn.
	//
	// StrategyRoundRobin 将所有请求轮流分发到各个模型。
	StrategyRoundRobin RouterStrategy = "round_robin"
	// StrategyWeightedRoundRobin distributes requests proportionally to each
	// route's Weight (default 1 when Weight <= 0).
	//
	// StrategyWeightedRoundRobin 按各路由 Weight（Weight <= 0 时默认 1）的
	// 比例分发请求。
	StrategyWeightedRoundRobin RouterStrategy = "weighted_round_robin"
	// StrategyRaceAll sends the request to all models simultaneously and returns
	// the first successful response.
	//
	// StrategyRaceAll 同时向所有模型发送请求，返回第一个成功的响应。
	StrategyRaceAll RouterStrategy = "race_all"
)

// ModelRoute defines a single model entry in the router.
//
// ModelRoute 定义路由器中的单个模型条目。
type ModelRoute struct {
	Model   kernel.Model
	Weight  int // used by weighted strategies; 0 = default 1
	Timeout int // optional per-model timeout in milliseconds; 0 = no override
	// CircuitBreaker is an optional circuit breaker for this route.
	CircuitBreaker kernel.CircuitBreaker
	// RateLimiter is an optional rate limiter for this route.
	RateLimiter kernel.RateLimiter
}

// ModelRouter implements ChatModel by delegating to multiple sub-models
// according to a configurable strategy.
//
// ModelRouter 实现 ChatModel，按可配置的策略将调用委托给多个子模型。
type ModelRouter struct {
	routes      []ModelRoute
	strategy    RouterStrategy
	middlewares []kernel.Middleware
	counter     atomic.Uint64
}

// ModelRouterOption configures a ModelRouter.
//
// ModelRouterOption 配置 ModelRouter。
type ModelRouterOption func(*ModelRouter)

// WithRouterStrategy sets the routing strategy.
//
// WithRouterStrategy 设置路由策略。
func WithRouterStrategy(s RouterStrategy) ModelRouterOption {
	return func(r *ModelRouter) { r.strategy = s }
}

// WithRouterMiddleware adds route-level middleware to the ModelRouter.
// Middleware is applied in order (outermost first) on top of per-route
// circuit breaker and rate limiter.
//
// For Runtime-level middleware (applied to ALL model calls), use
// kernel.WithMiddleware instead.
//
// WithRouterMiddleware 为 ModelRouter 添加路由级中间件。
// 中间件按传入顺序从外到内包装。路由级的熔断器和限流器在内层生效。
// 全局中间件请使用 kernel.WithMiddleware。
func WithRouterMiddleware(mws ...kernel.Middleware) ModelRouterOption {
	return func(r *ModelRouter) { r.middlewares = append(r.middlewares, mws...) }
}

// NewModelRouter creates a ModelRouter with one or more model routes.
// Default strategy is StrategyPriority.
//
// NewModelRouter 创建一个包含一个或多个模型路由的 ModelRouter。
// 默认策略为 StrategyPriority。
func NewModelRouter(routes []ModelRoute, opts ...ModelRouterOption) *ModelRouter {
	r := &ModelRouter{
		routes:   routes,
		strategy: StrategyPriority,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Generate routes the call according to the configured strategy.
//
// Generate 按配置的策略路由调用。
func (r *ModelRouter) Generate(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	switch r.strategy {
	case StrategyRoundRobin:
		return r.generateRoundRobin(ctx, messages, opts...)
	case StrategyWeightedRoundRobin:
		return r.generateWeighted(ctx, messages, opts...)
	case StrategyRaceAll:
		return r.generateRaceAll(ctx, messages, opts...)
	default:
		return r.generatePriority(ctx, messages, opts...)
	}
}

// Stream routes the call according to the configured strategy.
//
// Stream 按配置的策略路由流式调用。
func (r *ModelRouter) Stream(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	switch r.strategy {
	case StrategyRoundRobin:
		return r.streamRoundRobin(ctx, messages, opts...)
	case StrategyWeightedRoundRobin:
		return r.streamWeighted(ctx, messages, opts...)
	case StrategyRaceAll:
		return r.streamRaceAll(ctx, messages, opts...)
	default:
		return r.streamPriority(ctx, messages, opts...)
	}
}

// CountTokens estimates the number of tokens in the given messages by delegating
// to the first available route's model. Falls back to DefaultCountTokens if no routes.
//
// CountTokens 委托给第一个可用路由的模型估算给定消息的 token 数；
// 无路由时回退到 DefaultCountTokens。
func (r *ModelRouter) CountTokens(ctx context.Context, messages []*types.Message, opts ...kernel.GenOption) (int, error) {
	if len(r.routes) > 0 && r.routes[0].Model != nil {
		return r.routes[0].Model.CountTokens(ctx, messages, opts...)
	}
	return kernel.DefaultCountTokens(messages), nil
}

// applyRouteProtection applies circuit breaker, rate limiter, and global middlewares to a model call.
// asChatModel performs a safe type assertion. A route whose model does not
// implement kernel.ChatModel is a configuration error: it must surface as an
// error, never as a panic.
func asChatModel(route ModelRoute) (kernel.ChatModel, error) {
	m, ok := route.Model.(kernel.ChatModel)
	if !ok {
		return nil, fmt.Errorf("model router: route model %T does not implement kernel.ChatModel", route.Model)
	}
	return m, nil
}

// applyRouteProtection wraps a model handler with per-route protections:
// timeout (outermost), circuit breaker, rate limiter, then router middleware.
func applyRouteProtection(route ModelRoute, handler kernel.ModelHandler, mws []kernel.Middleware) kernel.ModelHandler {
	h := handler
	if route.Timeout > 0 {
		timeout := time.Duration(route.Timeout) * time.Millisecond
		inner := h
		h = func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
			tctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return inner(tctx, msgs)
		}
	}
	if route.CircuitBreaker != nil {
		h = kernel.NewCircuitBreakerMiddleware(route.CircuitBreaker).WrapGenerate(h)
	}
	if route.RateLimiter != nil {
		h = kernel.NewRateLimiterMiddleware(route.RateLimiter).WrapGenerate(h)
	}
	if len(mws) > 0 {
		h = kernel.Apply(h, mws...)
	}
	return h
}

// applyRouteProtectionStream is the streaming counterpart of applyRouteProtection.
func applyRouteProtectionStream(route ModelRoute, handler kernel.StreamHandler, mws []kernel.Middleware) kernel.StreamHandler {
	h := handler
	if route.Timeout > 0 {
		timeout := time.Duration(route.Timeout) * time.Millisecond
		inner := h
		h = func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
			tctx, cancel := context.WithTimeout(ctx, timeout)
			s, err := inner(tctx, msgs)
			if err != nil {
				cancel()
				return nil, err
			}
			// The timeout bounds stream OPENING; the stream's lifetime is
			// the caller's. Canceling here would kill the stream the moment
			// it is delivered (C1) — the context is released on Close.
			return &cancelOnClose{StreamReader: s, cancel: cancel}, nil
		}
	}
	if route.CircuitBreaker != nil {
		h = kernel.NewCircuitBreakerMiddleware(route.CircuitBreaker).WrapStream(h)
	}
	if route.RateLimiter != nil {
		h = kernel.NewRateLimiterMiddleware(route.RateLimiter).WrapStream(h)
	}
	if len(mws) > 0 {
		h = kernel.ApplyStream(h, mws...)
	}
	return h
}

// cancelOnClose releases the stream's context exactly when the caller closes
// the stream — the context must live as long as the stream, never shorter.
type cancelOnClose struct {
	kernel.StreamReader
	cancel context.CancelFunc
	once   sync.Once
}

// Close cancels the stream's context exactly once and closes the inner
// stream.
//
// Close 恰好一次地取消流的 context，并关闭内部流。
func (c *cancelOnClose) Close() error {
	c.once.Do(func() {
		c.cancel()
	})
	return c.StreamReader.Close()
}

// generatePriority tries each model in order; returns the first success.
func (r *ModelRouter) generatePriority(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (msg *types.Message, usage *types.TokenUsage, err error) {
	if len(r.routes) == 0 {
		return nil, nil, ErrNoRoutes
	}
	var errs []error
	for i, route := range r.routes {
		model, cerr := asChatModel(route)
		if cerr != nil {
			errs = append(errs, cerr)
			continue
		}
		handler := applyRouteProtection(route, func(ctx context.Context, messages []*types.Message) (*types.Message, *types.TokenUsage, error) {
			return model.Generate(ctx, messages, opts...)
		}, r.middlewares)
		m, u, e := handler(ctx, msgs)
		if e == nil {
			return m, u, nil
		}
		errs = append(errs, fmt.Errorf("model[%d:%T]: %w", i, route.Model, e))
	}
	return nil, nil, fmt.Errorf("model router: all %d models failed: %w", len(r.routes), errors.Join(errs...))
}

func (r *ModelRouter) streamPriority(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	if len(r.routes) == 0 {
		return nil, ErrNoRoutes
	}
	var errs []error
	for i, route := range r.routes {
		model, cerr := asChatModel(route)
		if cerr != nil {
			errs = append(errs, cerr)
			continue
		}
		handler := applyRouteProtectionStream(route, func(ctx context.Context, messages []*types.Message) (kernel.StreamReader, error) {
			return model.Stream(ctx, messages, opts...)
		}, r.middlewares)
		s, e := handler(ctx, msgs)
		if e == nil {
			return s, nil
		}
		errs = append(errs, fmt.Errorf("model[%d:%T]: %w", i, route.Model, e))
	}
	return nil, fmt.Errorf("model router: all %d models failed: %w", len(r.routes), errors.Join(errs...))
}

func (r *ModelRouter) generateRoundRobin(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	if len(r.routes) == 0 {
		return nil, nil, ErrNoRoutes
	}
	idx := int(r.counter.Add(1) % uint64(len(r.routes)))
	route := r.routes[idx]
	model, err := asChatModel(route)
	if err != nil {
		return nil, nil, err
	}
	handler := applyRouteProtection(route, func(ctx context.Context, messages []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return model.Generate(ctx, messages, opts...)
	}, r.middlewares)
	return handler(ctx, msgs)
}

func (r *ModelRouter) streamRoundRobin(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	if len(r.routes) == 0 {
		return nil, ErrNoRoutes
	}
	idx := int(r.counter.Add(1) % uint64(len(r.routes)))
	route := r.routes[idx]
	model, err := asChatModel(route)
	if err != nil {
		return nil, err
	}
	handler := applyRouteProtectionStream(route, func(ctx context.Context, messages []*types.Message) (kernel.StreamReader, error) {
		return model.Stream(ctx, messages, opts...)
	}, r.middlewares)
	return handler(ctx, msgs)
}

// pickWeighted selects a route proportionally to its Weight (default 1).
// The shared atomic counter keeps the distribution smooth across callers.
func (r *ModelRouter) pickWeighted() (ModelRoute, error) {
	if len(r.routes) == 0 {
		return ModelRoute{}, ErrNoRoutes
	}
	total := 0
	for _, route := range r.routes {
		w := route.Weight
		if w <= 0 {
			w = 1
		}
		total += w
	}
	n := int(r.counter.Add(1) % uint64(total))
	for _, route := range r.routes {
		w := route.Weight
		if w <= 0 {
			w = 1
		}
		if n < w {
			return route, nil
		}
		n -= w
	}
	return r.routes[0], nil // unreachable: n < total always
}

func (r *ModelRouter) generateWeighted(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	route, err := r.pickWeighted()
	if err != nil {
		return nil, nil, err
	}
	model, err := asChatModel(route)
	if err != nil {
		return nil, nil, err
	}
	handler := applyRouteProtection(route, func(ctx context.Context, messages []*types.Message) (*types.Message, *types.TokenUsage, error) {
		return model.Generate(ctx, messages, opts...)
	}, r.middlewares)
	return handler(ctx, msgs)
}

func (r *ModelRouter) streamWeighted(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	route, err := r.pickWeighted()
	if err != nil {
		return nil, err
	}
	model, err := asChatModel(route)
	if err != nil {
		return nil, err
	}
	handler := applyRouteProtectionStream(route, func(ctx context.Context, messages []*types.Message) (kernel.StreamReader, error) {
		return model.Stream(ctx, messages, opts...)
	}, r.middlewares)
	return handler(ctx, msgs)
}

// generateRaceAll sends the request to all models simultaneously and returns
// the first successful response; cancels the rest.
func (r *ModelRouter) generateRaceAll(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	if len(r.routes) == 0 {
		return nil, nil, ErrNoRoutes
	}
	type result struct {
		msg   *types.Message
		usage *types.TokenUsage
		err   error
	}
	ch := make(chan result, len(r.routes))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, route := range r.routes {
		route := route
		model, cerr := asChatModel(route)
		if cerr != nil {
			ch <- result{err: cerr}
			continue
		}
		go func() {
			handler := applyRouteProtection(route, func(ctx context.Context, messages []*types.Message) (*types.Message, *types.TokenUsage, error) {
				return model.Generate(ctx, messages, opts...)
			}, r.middlewares)
			m, u, e := handler(ctx, msgs)
			select {
			case ch <- result{m, u, e}:
			case <-ctx.Done():
			}
		}()
	}

	// Collect all results. On first success, cancel remaining and return it.
	// This relies on model implementations respecting context cancellation.
	var firstErr error
	for i := 0; i < len(r.routes); i++ {
		select {
		case res := <-ch:
			if res.err == nil {
				cancel()
				return res.msg, res.usage, nil
			}
			if firstErr == nil {
				firstErr = res.err
			}
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	return nil, nil, fmt.Errorf("model router: race all %d models failed: %w", len(r.routes), firstErr)
}

// streamRaceAll races all routes and returns the first successfully opened stream.
func (r *ModelRouter) streamRaceAll(ctx context.Context, msgs []*types.Message, opts ...kernel.GenOption) (kernel.StreamReader, error) {
	if len(r.routes) == 0 {
		return nil, ErrNoRoutes
	}
	type result struct {
		stream kernel.StreamReader
		err    error
		cancel context.CancelFunc
		idx    int
	}
	ch := make(chan result, len(r.routes))

	// Each route races under its OWN derived context: the winner's context
	// must outlive the race (C1 — a shared cancel killed the winning
	// stream); losers are canceled individually once a winner exists.
	cancels := make([]context.CancelFunc, 0, len(r.routes))
	cancelExcept := func(keep int) {
		for i, c := range cancels {
			if i != keep {
				c()
			}
		}
	}

	for _, route := range r.routes {
		route := route
		model, cerr := asChatModel(route)
		if cerr != nil {
			ch <- result{err: cerr, idx: -1}
			continue
		}
		rctx, rcancel := context.WithCancel(ctx)
		idx := len(cancels)
		cancels = append(cancels, rcancel)
		go func() {
			handler := applyRouteProtectionStream(route, func(ctx context.Context, messages []*types.Message) (kernel.StreamReader, error) {
				return model.Stream(ctx, messages, opts...)
			}, r.middlewares)
			s, e := handler(rctx, msgs)
			select {
			case ch <- result{s, e, rcancel, idx}:
			case <-rctx.Done():
			}
		}()
	}

	// Collect until a stream opens successfully. The winner keeps its own
	// context, released on Close; every loser is canceled immediately.
	var firstErr error
	for i := 0; i < len(r.routes); i++ {
		select {
		case res := <-ch:
			if res.err == nil {
				cancelExcept(res.idx)
				return &cancelOnClose{StreamReader: res.stream, cancel: res.cancel}, nil
			}
			if firstErr == nil {
				firstErr = res.err
			}
		case <-ctx.Done():
			cancelExcept(-1)
			return nil, ctx.Err()
		}
	}

	cancelExcept(-1)
	return nil, fmt.Errorf("model router: race all %d models failed: %w", len(r.routes), firstErr)
}
