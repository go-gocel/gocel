package kernel

import (
	"context"
	"sync"
)

// AsyncIterator provides a channel-based event stream for consuming agent output.
// AsyncIterator 提供基于通道的事件流，用于消费 Agent 输出。
type AsyncIterator[T any] struct {
	ch     chan T
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	err    error
}

// NewAsyncIterator creates an AsyncIterator with the given buffer size,
// defaulting to 100 when bufferSize <= 0.
// NewAsyncIterator 创建 AsyncIterator，指定缓冲大小（bufferSize <= 0 时默认为 100）。
func NewAsyncIterator[T any](bufferSize int) *AsyncIterator[T] {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	return &AsyncIterator[T]{
		ch:   make(chan T, bufferSize),
		done: make(chan struct{}),
	}
}

// NewAsyncIteratorWithContext creates an AsyncIterator bound to ctx; the
// iterator closes automatically when the context is done.
// NewAsyncIteratorWithContext 创建绑定 ctx 的 AsyncIterator；context 结束时
// 迭代器自动关闭。
func NewAsyncIteratorWithContext[T any](ctx context.Context, bufferSize int) *AsyncIterator[T] {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	ctx, cancel := context.WithCancel(ctx)
	it := &AsyncIterator[T]{
		ch:     make(chan T, bufferSize),
		done:   make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
	}
	go it.watchContext()
	return it
}

func (it *AsyncIterator[T]) watchContext() {
	select {
	case <-it.ctx.Done():
	case <-it.done:
		return
	}
	it.mu.Lock()
	defer it.mu.Unlock()
	if !it.closed {
		it.closed = true
		it.err = it.ctx.Err()
		if it.cancel != nil {
			it.cancel()
		}
		close(it.done)
		close(it.ch)
	}
}

// NewAsyncIteratorPair creates an AsyncIterator paired with its write-side
// AsyncGenerator.
// NewAsyncIteratorPair 创建 AsyncIterator 及其配套的写入端 AsyncGenerator。
func NewAsyncIteratorPair[T any]() (*AsyncIterator[T], *AsyncGenerator[T]) {
	it := NewAsyncIterator[T](100)
	return it, &AsyncGenerator[T]{it: it}
}

// NewAsyncIteratorPairWithContext creates a context-bound AsyncIterator paired
// with its write-side AsyncGenerator.
// NewAsyncIteratorPairWithContext 创建绑定 context 的 AsyncIterator 及其
// 配套的写入端 AsyncGenerator。
func NewAsyncIteratorPairWithContext[T any](ctx context.Context) (*AsyncIterator[T], *AsyncGenerator[T]) {
	it := NewAsyncIteratorWithContext[T](ctx, 100)
	return it, &AsyncGenerator[T]{it: it}
}

// Send pushes a value into the stream. It returns false when the iterator
// is closed.
// Send 向流中推送一个值；迭代器已关闭时返回 false。
func (it *AsyncIterator[T]) Send(v T) (ok bool) {
	// Fast path: check closed flag under mutex.
	it.mu.Lock()
	if it.closed {
		it.mu.Unlock()
		return false
	}
	it.mu.Unlock()

	// Guard against the race where Close closes ch between the mutex check
	// and the select (Go select picks randomly among ready cases).
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	select {
	case it.ch <- v:
		return true
	case <-it.done:
		return false
	}
}

// Next reads the next value from the stream; ok is false once the stream
// is closed and drained.
// Next 从流中读取下一个值；流已关闭且耗尽后 ok 为 false。
func (it *AsyncIterator[T]) Next() (T, bool) {
	v, ok := <-it.ch
	return v, ok
}

// Channel returns the underlying receive-only channel.
// Channel 返回底层只读通道。
func (it *AsyncIterator[T]) Channel() <-chan T {
	return it.ch
}

// Ch is an alias of Channel, returning the underlying receive-only channel.
// Ch 是 Channel 的别名，返回底层只读通道。
func (it *AsyncIterator[T]) Ch() <-chan T {
	return it.ch
}

// Close closes the iterator, unblocking any pending Send and terminating
// the stream.
// Close 关闭迭代器，解除阻塞中的 Send 并终止流。
func (it *AsyncIterator[T]) Close() {
	it.mu.Lock()
	defer it.mu.Unlock()
	if !it.closed {
		it.closed = true
		if it.cancel != nil {
			it.cancel()
		}
		close(it.done)
		close(it.ch)
	}
}

// CloseWithError closes the iterator and records the error for Err().
// Transforms close their outputs with it so failures surface to consumers
// instead of presenting as a clean end-of-stream.
//
// CloseWithError 关闭迭代器并记录错误供 Err() 读取。变换层用它关闭输出，
// 使失败对消费方可见，而不是伪装成干净的流结束。
func (it *AsyncIterator[T]) CloseWithError(err error) {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.err = err
	if !it.closed {
		it.closed = true
		if it.cancel != nil {
			it.cancel()
		}
		close(it.done)
		close(it.ch)
	}
}

// Collect drains the stream and returns all remaining values.
// Collect 排空流并返回所有剩余值。
func (it *AsyncIterator[T]) Collect() []T {
	var result []T
	for v := range it.ch {
		result = append(result, v)
	}
	return result
}

// Done returns a channel that is closed when the iterator is closed.
// Done 返回一个通道；迭代器关闭时该通道被关闭。
func (it *AsyncIterator[T]) Done() <-chan struct{} {
	return it.done
}

// Err returns the error recorded at close time, or nil if none.
// Err 返回关闭时记录的错误；没有错误时返回 nil。
func (it *AsyncIterator[T]) Err() error {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.err
}

// AsyncGenerator is the write side of an iterator, used to push data into
// the paired AsyncIterator.
// AsyncGenerator 是迭代器的写入端，用于向关联的 AsyncIterator 推送数据。
type AsyncGenerator[T any] struct {
	it *AsyncIterator[T]
}

// Send pushes a value into the paired iterator; returns false when closed.
// Send 向配套的迭代器推送一个值；已关闭时返回 false。
func (g *AsyncGenerator[T]) Send(v T) bool {
	return g.it.Send(v)
}

// Close closes the paired iterator.
// Close 关闭配套的迭代器。
func (g *AsyncGenerator[T]) Close() {
	g.it.Close()
}

// CloseWithError closes the paired iterator and records the error.
// CloseWithError 关闭配套的迭代器并记录错误。
func (g *AsyncGenerator[T]) CloseWithError(err error) {
	g.it.CloseWithError(err)
}

// Iterate runs fn in a goroutine and sends its results through the
// AsyncGenerator into the returned iterator.
// Iterate 在 goroutine 中执行一个函数，并将结果通过 AsyncGenerator 发送。
func Iterate[T any](ctx context.Context, bufferSize int, fn func(ctx context.Context, ch chan<- T) error) *AsyncIterator[T] {
	it := NewAsyncIteratorWithContext[T](ctx, bufferSize)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				_ = r
			}
			it.Close()
		}()
		if err := fn(ctx, it.ch); err != nil {
			it.mu.Lock()
			it.err = err
			it.mu.Unlock()
		}
	}()
	return it
}
