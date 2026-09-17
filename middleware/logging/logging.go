// Package logging provides a gocel Middleware that logs model call timing.
package logging

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Middleware is a gocel Middleware that logs model call timing.
//
// Middleware 是记录模型调用耗时的 gocel 中间件。
type Middleware struct{ Logger *log.Logger }

// New creates a logging middleware; a nil logger defaults to stderr with
// the "[model] " prefix.
//
// New 创建日志中间件；logger 为 nil 时默认输出到 stderr，前缀为
// "[model] "。
func New(logger *log.Logger) *Middleware {
	if logger == nil {
		logger = log.New(os.Stderr, "[model] ", log.LstdFlags)
	}
	return &Middleware{Logger: logger}
}

// Name returns the middleware name.
//
// Name 返回中间件名称。
func (m *Middleware) Name() string { return "logging" }

// WrapGenerate wraps a model handler, logging the elapsed time and token
// usage of each call.
//
// WrapGenerate 包装模型处理器，记录每次调用的耗时与 token 用量。
func (m *Middleware) WrapGenerate(next kernel.ModelHandler) kernel.ModelHandler {
	return func(ctx context.Context, msgs []*types.Message) (*types.Message, *types.TokenUsage, error) {
		start := time.Now()
		resp, usage, err := next(ctx, msgs)
		elapsed := time.Since(start)
		if err != nil {
			m.Logger.Printf("generate ERROR after %v: %v", elapsed, err)
		} else if usage != nil {
			m.Logger.Printf("generate OK    %v | in=%d out=%d total=%d",
				elapsed, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
		}
		return resp, usage, err
	}
}

// WrapStream wraps a streaming handler, logging the stream start and
// completion (OK / ERROR / ABORT) events.
//
// WrapStream 包装流式处理器，记录流开始与完成（OK / ERROR / ABORT）事件。
func (m *Middleware) WrapStream(next kernel.StreamHandler) kernel.StreamHandler {
	return func(ctx context.Context, msgs []*types.Message) (kernel.StreamReader, error) {
		start := time.Now()
		m.Logger.Printf("stream  START")
		reader, err := next(ctx, msgs)
		if err != nil {
			m.Logger.Printf("stream  ERROR after %v: %v", time.Since(start), err)
			return nil, err
		}
		return &loggingStreamReader{inner: reader, logger: m.Logger, start: start}, nil
	}
}

// loggingStreamReader 包装底层 reader，在流结束（EOF / 提前关闭 / 错误）时
// 补记完成事件（耗时 / 字符数 / token 用量）。
type loggingStreamReader struct {
	inner  kernel.StreamReader
	logger *log.Logger
	start  time.Time

	usage  *types.TokenUsage
	chars  int64
	logged bool
}

// Recv returns the next stream message, accumulating character and usage
// totals for the completion log entry.
//
// Recv 返回下一条流式消息，累计字符数与 token 用量供完成日志使用。
func (r *loggingStreamReader) Recv() (*types.Message, error) {
	chunk, err := r.inner.Recv()
	if chunk != nil {
		r.chars += int64(len(chunk.Content))
		if chunk.Meta != nil {
			if u, ok := chunk.Meta["usage"]; ok {
				if usage, ok := u.(*types.TokenUsage); ok {
					r.usage = usage
				}
			}
		}
	}
	if err != nil {
		r.logDone(err)
	}
	return chunk, err
}

// Close closes the inner stream and logs an abort entry when the stream
// did not reach EOF.
//
// Close 关闭内部流；未到 EOF 即关闭时记录中止日志。
func (r *loggingStreamReader) Close() error {
	err := r.inner.Close()
	// 未到 EOF 即关闭 → 记录中止。
	r.logDone(nil)
	return err
}

// Done returns the inner stream's done channel.
//
// Done 返回内部流的完成 channel。
func (r *loggingStreamReader) Done() <-chan struct{} { return r.inner.Done() }

// logDone records the completion entry exactly once.
func (r *loggingStreamReader) logDone(err error) {
	if r.logged {
		return
	}
	r.logged = true
	elapsed := time.Since(r.start)
	switch {
	case err != nil && !errors.Is(err, io.EOF):
		r.logger.Printf("stream  ERROR after %v: %v", elapsed, err)
	case err != nil: // io.EOF — 完整读完
		if r.usage != nil {
			r.logger.Printf("stream  OK    %v | chars=%d in=%d out=%d total=%d",
				elapsed, r.chars, r.usage.PromptTokens, r.usage.CompletionTokens, r.usage.TotalTokens)
		} else {
			r.logger.Printf("stream  OK    %v | chars=%d", elapsed, r.chars)
		}
	default: // 提前 Close
		r.logger.Printf("stream  ABORT %v | chars=%d", elapsed, r.chars)
	}
}
