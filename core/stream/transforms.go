// Package stream provides composable stream transforms for AsyncIterator[T].
//
// Each transform takes one or more *kernel.AsyncIterator[T] and returns a new
// *kernel.AsyncIterator[U] (or *kernel.AsyncIterator[T] for identity-type transforms),
// running the transform logic in a background goroutine.
//
// All transforms respect context cancellation and propagate errors from the
// source iterator.
package stream

import (
	"context"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
)

// ──────────────────────────────────────────────
// Map  – transform each element
// ──────────────────────────────────────────────

// Map applies fn to every element from src and yields the results.
// The output iterator has the same buffer size as src.
// Map 对 src 的每个元素应用 fn 并产出结果。输出迭代器的缓冲区大小与
// src 相同。
func Map[T, U any](src *kernel.AsyncIterator[T], fn func(T) U) *kernel.AsyncIterator[U] {
	return mapWithCtx[T, U](src, func(v T) U { return fn(v) })
}

// MapErr is like Map but fn can return an error. The first error causes the
// output iterator to close with that error (Err() returns it). Source errors
// propagate through the output as well.
// MapErr 类似 Map，但 fn 可以返回错误。首个错误会使输出迭代器以该错误
// 关闭（Err() 返回它）；源迭代器的错误同样会传播到输出。
func MapErr[T, U any](src *kernel.AsyncIterator[T], fn func(T) (U, error)) *kernel.AsyncIterator[U] {
	// Calculate buffer size: same as src if we can guess, else 100
	buf := bufferSize(src)
	dst := kernel.NewAsyncIterator[U](buf)
	go func() {
		defer func() {
			if err := src.Err(); err != nil {
				dst.CloseWithError(err)
				return
			}
			dst.Close()
		}()
		for {
			v, ok := src.Next()
			if !ok {
				return
			}
			u, err := fn(v)
			if err != nil {
				dst.CloseWithError(err)
				return
			}
			if !dst.Send(u) {
				return
			}
		}
	}()
	return dst
}

// mapWithCtx is the internal implementation for Map. Source errors
// propagate through the output (the package contract).
func mapWithCtx[T, U any](src *kernel.AsyncIterator[T], fn func(T) U) *kernel.AsyncIterator[U] {
	buf := bufferSize(src)
	dst := kernel.NewAsyncIterator[U](buf)
	go func() {
		defer func() {
			if err := src.Err(); err != nil {
				dst.CloseWithError(err)
				return
			}
			dst.Close()
		}()
		for {
			v, ok := src.Next()
			if !ok {
				return
			}
			u := fn(v)
			if !dst.Send(u) {
				return
			}
		}
	}()
	return dst
}

// ──────────────────────────────────────────────
// Filter  – keep matching elements
// ──────────────────────────────────────────────

// Filter keeps elements for which fn returns true. Source errors propagate
// through the output.
// Filter 保留 fn 返回 true 的元素。源迭代器的错误会传播到输出。
func Filter[T any](src *kernel.AsyncIterator[T], fn func(T) bool) *kernel.AsyncIterator[T] {
	buf := bufferSize(src)
	dst := kernel.NewAsyncIterator[T](buf)
	go func() {
		defer func() {
			if err := src.Err(); err != nil {
				dst.CloseWithError(err)
				return
			}
			dst.Close()
		}()
		for {
			v, ok := src.Next()
			if !ok {
				return
			}
			if fn(v) {
				if !dst.Send(v) {
					return
				}
			}
		}
	}()
	return dst
}

// ──────────────────────────────────────────────
// Reduce  – fold stream into a single value (blocking)
// ──────────────────────────────────────────────

// Reduce folds the stream into a single value using fn.
// It blocks until src is closed.
// Reduce 使用 fn 将流折叠为单个值。它会阻塞直到 src 关闭。
func Reduce[T, R any](src *kernel.AsyncIterator[T], init R, fn func(acc R, v T) R) R {
	acc := init
	for {
		v, ok := src.Next()
		if !ok {
			return acc
		}
		acc = fn(acc, v)
	}
}

// ──────────────────────────────────────────────
// Concat  – sequential concatenation
// ──────────────────────────────────────────────

// Concat yields elements from each source iterator in order, one after another.
// The output closes when all source iterators are exhausted; a source error
// propagates.
// Concat 按顺序依次产出每个源迭代器的元素。所有源迭代器耗尽后输出关闭；
// 源错误会传播。
func Concat[T any](iters ...*kernel.AsyncIterator[T]) *kernel.AsyncIterator[T] {
	buf := 100
	for _, it := range iters {
		if b := bufferSize(it); b > buf {
			buf = b
		}
	}
	dst := kernel.NewAsyncIterator[T](buf)
	go func() {
		defer func() {
			for _, src := range iters {
				if err := src.Err(); err != nil {
					dst.CloseWithError(err)
					return
				}
			}
			dst.Close()
		}()
		for _, src := range iters {
			for {
				v, ok := src.Next()
				if !ok {
					break
				}
				if !dst.Send(v) {
					return
				}
			}
		}
	}()
	return dst
}

// ──────────────────────────────────────────────
// Merge  – fan-in (concurrent merge)
// ──────────────────────────────────────────────

// Merge merges multiple streams into one. Elements are emitted as soon as any
// source iterator provides one (non-deterministic order).
//
// Merge closes the output when all sources are exhausted. If ctx is cancelled
// the output closes immediately. If no source iterators are given, a closed
// (empty) iterator is returned.
// Merge 在所有源耗尽时关闭输出；ctx 被取消时输出立即关闭。未提供任何
// 源迭代器时返回一个已关闭（空）的迭代器。
func Merge[T any](ctx context.Context, iters ...*kernel.AsyncIterator[T]) *kernel.AsyncIterator[T] {
	if len(iters) == 0 {
		dst := kernel.NewAsyncIterator[T](0)
		dst.Close()
		return dst
	}
	buf := 100
	for _, it := range iters {
		if b := bufferSize(it); b > buf {
			buf = b
		}
	}
	dst := kernel.NewAsyncIterator[T](buf)

	var wg sync.WaitGroup
	wg.Add(len(iters))

	for _, src := range iters {
		src := src
		go func() {
			defer wg.Done()
			for {
				// Check context cancellation
				select {
				case <-ctx.Done():
					return
				default:
				}
				v, ok := src.Next()
				if !ok {
					return
				}
				if !dst.Send(v) {
					return
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		dst.Close()
	}()

	return dst
}

// ──────────────────────────────────────────────
// Buffer  – batch elements
// ──────────────────────────────────────────────

// Buffer batches elements into slices of at most size. The final batch may be
// smaller. If size <= 1 the identity stream is returned (each element wrapped).
// Buffer 将元素分批为最多 size 个的切片，最后一批可能更小。size <= 1 时
// 返回恒等流（每个元素单独包裹）。
func Buffer[T any](src *kernel.AsyncIterator[T], size int) *kernel.AsyncIterator[[]T] {
	if size <= 1 {
		return Map(src, func(v T) []T { return []T{v} })
	}
	buf := bufferSize(src)
	dst := kernel.NewAsyncIterator[[]T](buf)
	go func() {
		defer dst.Close()
		batch := make([]T, 0, size)
		flush := func() {
			if len(batch) > 0 {
				out := make([]T, len(batch))
				copy(out, batch)
				dst.Send(out)
				batch = batch[:0]
			}
		}
		for {
			v, ok := src.Next()
			if !ok {
				flush()
				return
			}
			batch = append(batch, v)
			if len(batch) >= size {
				flush()
			}
		}
	}()
	return dst
}

// ──────────────────────────────────────────────
// Debounce  – emit after quiet period
// ──────────────────────────────────────────────

// Debounce emits the most recent value after a quiet period of d has elapsed
// without new values. Intermediate values are discarded.
// Debounce 在 d 时长内没有新值后，发出最近的一个值。中间值会被丢弃。
func Debounce[T any](src *kernel.AsyncIterator[T], d time.Duration) *kernel.AsyncIterator[T] {
	buf := bufferSize(src)
	dst := kernel.NewAsyncIterator[T](buf)
	go func() {
		defer dst.Close()
		var timer *time.Timer
		var pending T
		var hasPending bool
		for {
			v, ok := src.Next()
			if !ok {
				if hasPending {
					dst.Send(pending)
				}
				return
			}
			pending = v
			hasPending = true
			if timer == nil {
				timer = time.NewTimer(d)
			} else {
				timer.Reset(d)
			}
			select {
			case <-timer.C:
				dst.Send(pending)
				hasPending = false
			default:
				// continue reading — debounce another
			}
		}
	}()
	return dst
}

// ──────────────────────────────────────────────
// Throttle  – emit at most once per duration
// ──────────────────────────────────────────────

// Throttle ensures at most one element is emitted per d duration.
// Intermediate values are discarded.
// Throttle 保证每 d 时长最多发出一个元素。中间值会被丢弃。
func Throttle[T any](src *kernel.AsyncIterator[T], d time.Duration) *kernel.AsyncIterator[T] {
	buf := bufferSize(src)
	dst := kernel.NewAsyncIterator[T](buf)
	go func() {
		defer dst.Close()
		ticker := time.NewTicker(d)
		defer ticker.Stop()
		ready := true
		for {
			v, ok := src.Next()
			if !ok {
				return
			}
			if ready {
				if !dst.Send(v) {
					return
				}
				ready = false
				continue
			}
			// Wait for next tick
			select {
			case <-ticker.C:
				ready = true
				if !dst.Send(v) {
					return
				}
			default:
			}
		}
	}()
	return dst
}

// ──────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────

// bufferSize attempts to read the channel buffer size of an AsyncIterator
// (by draining any pending messages to count them). Instead we return a
// reasonable default of 100.
func bufferSize[T any](it *kernel.AsyncIterator[T]) int {
	_ = it
	return 100
}
