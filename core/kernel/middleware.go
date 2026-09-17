// Package kernel defines all middleware interfaces for model call decoration.
//
// 层级位置：中间件位于 Runtime 内部、ChatModel 之上，只包裹模型调用
// （Generate/Stream，msgs → resp）。中间件不可见循环、工具与状态；
// 观察/干预整个生命周期的职责属于 Module 钩子层（Runtime 之上）。
// 典型用例：重试、限流、熔断、日志、追踪。
//
// Middleware follows the onion model: outer middleware wraps inner ones.
// Each middleware can intercept both Generate and Stream calls.
//
// LAYER: inside the Runtime, directly above ChatModel. Middleware only sees
// model calls (msgs → resp); observing/intervening in the whole lifecycle
// is the Module hook layer's job (above the Runtime).
package kernel

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// Middleware wraps a model call with cross-cutting behavior.
//
// Implementations should be safe for concurrent use.
// Use NewFuncMiddleware for simple function-based middleware.
//
// Middleware 是模型调用的中间件接口，遵循洋葱模型。
// 实现需要保证并发安全。简单场景可用 NewFuncMiddleware。
type Middleware interface {
	// Name returns the middleware identifier (for logging, tracing).
	// Name 返回中间件名称，用于日志和追踪。
	Name() string

	// WrapGenerate wraps a ModelHandler with pre/post logic.
	// WrapGenerate 包装 ModelHandler，添加前置/后置逻辑。
	WrapGenerate(next ModelHandler) ModelHandler

	// WrapStream wraps a StreamHandler with pre/post logic.
	// WrapStream 包装 StreamHandler，添加前置/后置逻辑。
	WrapStream(next StreamHandler) StreamHandler
}

// FuncMiddleware adapts functions to the Middleware interface.
//
// Usage:
//
//	mw := NewFuncMiddleware("name",
//	    func(next ModelHandler) ModelHandler {
//	        return func(ctx context.Context, msgs []*Message) (*Message, *TokenUsage, error) {
//	            // pre
//	            resp, usage, err := next(ctx, msgs)
//	            // post
//	            return resp, usage, err
//	        }
//	    },
//	    func(next StreamHandler) StreamHandler {
//	        return func(ctx context.Context, msgs []*Message) (StreamReader, error) {
//	            return next(ctx, msgs)
//	        }
//	    },
//	)
//
// FuncMiddleware 将函数适配为 Middleware 接口。
type FuncMiddleware struct {
	name         string
	wrapGenerate func(ModelHandler) ModelHandler
	wrapStream   func(StreamHandler) StreamHandler
}

// NewFuncMiddleware creates a FuncMiddleware.
// Passing nil for gen or stream skips that call type.
//
// NewFuncMiddleware 创建 FuncMiddleware。gen 或 stream 传 nil 则跳过对应类型。
func NewFuncMiddleware(name string,
	gen func(ModelHandler) ModelHandler,
	stream func(StreamHandler) StreamHandler,
) *FuncMiddleware {
	return &FuncMiddleware{
		name:         name,
		wrapGenerate: gen,
		wrapStream:   stream,
	}
}

// Name returns the middleware name.
// Name 返回中间件名称。
func (f *FuncMiddleware) Name() string { return f.name }

// WrapGenerate wraps a ModelHandler.
// WrapGenerate 包装 ModelHandler。
func (f *FuncMiddleware) WrapGenerate(next ModelHandler) ModelHandler {
	if f.wrapGenerate == nil {
		return next
	}
	return f.wrapGenerate(next)
}

// WrapStream wraps a StreamHandler.
// WrapStream 包装 StreamHandler。
func (f *FuncMiddleware) WrapStream(next StreamHandler) StreamHandler {
	if f.wrapStream == nil {
		return next
	}
	return f.wrapStream(next)
}

// Apply chains middleware around a ModelHandler (outermost first).
// Apply 将中间件链按从外到内的顺序包装 ModelHandler。
func Apply(handler ModelHandler, mws ...Middleware) ModelHandler {
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] != nil {
			handler = mws[i].WrapGenerate(handler)
		}
	}
	return handler
}

// ApplyStream chains middleware around a StreamHandler (outermost first).
// ApplyStream 将中间件链按从外到内的顺序包装 StreamHandler。
func ApplyStream(handler StreamHandler, mws ...Middleware) StreamHandler {
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] != nil {
			handler = mws[i].WrapStream(handler)
		}
	}
	return handler
}

// NoopMiddleware is a no-op middleware that passes through all calls.
//
// NoopMiddleware 是空操作中间件，透传所有调用。
type NoopMiddleware struct{}

// Name returns "noop".
// Name 返回 "noop"。
func (NoopMiddleware) Name() string { return "noop" }

// WrapGenerate returns next unchanged.
// WrapGenerate 原样返回 next。
func (NoopMiddleware) WrapGenerate(next ModelHandler) ModelHandler { return next }

// WrapStream returns next unchanged.
// WrapStream 原样返回 next。
func (NoopMiddleware) WrapStream(next StreamHandler) StreamHandler { return next }
