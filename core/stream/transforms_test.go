package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
)

// ──────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────

func collect[T any](it *kernel.AsyncIterator[T]) []T {
	var out []T
	for {
		v, ok := it.Next()
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

func fromSlice[T any](vals []T) *kernel.AsyncIterator[T] {
	it := kernel.NewAsyncIterator[T](len(vals))
	go func() {
		for _, v := range vals {
			it.Send(v)
		}
		it.Close()
	}()
	return it
}

// ──────────────────────────────────────────────
// Map
// ──────────────────────────────────────────────

func TestMap(t *testing.T) {
	t.Run("double integers", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3, 4})
		out := Map(src, func(v int) int { return v * 2 })
		got := collect(out)
		want := []int{2, 4, 6, 8}
		if !equal(got, want) {
			t.Fatalf("Map: got %v, want %v", got, want)
		}
	})

	t.Run("change type", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3})
		out := Map(src, func(v int) string { return string(rune('A' + v - 1)) })
		got := collect(out)
		want := []string{"A", "B", "C"}
		if !equal(got, want) {
			t.Fatalf("Map type change: got %v, want %v", got, want)
		}
	})

	t.Run("empty stream", func(t *testing.T) {
		src := kernel.NewAsyncIterator[int](10)
		src.Close()
		out := Map(src, func(v int) int { return v })
		got := collect(out)
		if len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})
}

func TestMapErr(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3})
		out := MapErr(src, func(v int) (int, error) { return v * 2, nil })
		got := collect(out)
		want := []int{2, 4, 6}
		if !equal(got, want) {
			t.Fatalf("MapErr: got %v, want %v", got, want)
		}
	})

	t.Run("error stops stream", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3, 4, 5})
		errSentinel := errors.New("oops")
		out := MapErr(src, func(v int) (int, error) {
			if v > 3 {
				return 0, errSentinel
			}
			return v, nil
		})
		got := collect(out)
		// Elements before error may or may not all arrive; at minimum we get 1,2,3
		if len(got) < 3 {
			t.Fatalf("expected at least 3 elements, got %v", got)
		}
	})
}

// ──────────────────────────────────────────────
// Filter
// ──────────────────────────────────────────────

func TestFilter(t *testing.T) {
	t.Run("even numbers", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3, 4, 5, 6})
		out := Filter(src, func(v int) bool { return v%2 == 0 })
		got := collect(out)
		want := []int{2, 4, 6}
		if !equal(got, want) {
			t.Fatalf("Filter: got %v, want %v", got, want)
		}
	})

	t.Run("all pass", func(t *testing.T) {
		src := fromSlice([]int{1, 2})
		out := Filter(src, func(v int) bool { return true })
		got := collect(out)
		if !equal(got, []int{1, 2}) {
			t.Fatalf("Filter all pass: got %v", got)
		}
	})

	t.Run("none pass", func(t *testing.T) {
		src := fromSlice([]int{1, 2})
		out := Filter(src, func(v int) bool { return false })
		got := collect(out)
		if len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})
}

// ──────────────────────────────────────────────
// Reduce
// ──────────────────────────────────────────────

func TestReduce(t *testing.T) {
	t.Run("sum", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3, 4, 5})
		sum := Reduce(src, 0, func(acc, v int) int { return acc + v })
		if sum != 15 {
			t.Fatalf("Reduce sum: got %d, want 15", sum)
		}
	})

	t.Run("empty stream", func(t *testing.T) {
		src := kernel.NewAsyncIterator[int](10)
		src.Close()
		sum := Reduce(src, 42, func(acc, v int) int { return acc + v })
		if sum != 42 {
			t.Fatalf("Reduce empty: got %d, want 42", sum)
		}
	})

	t.Run("string concat", func(t *testing.T) {
		src := fromSlice([]string{"a", "b", "c"})
		result := Reduce(src, "", func(acc, v string) string { return acc + v })
		if result != "abc" {
			t.Fatalf("Reduce concat: got %q, want %q", result, "abc")
		}
	})
}

// ──────────────────────────────────────────────
// Concat
// ──────────────────────────────────────────────

func TestConcat(t *testing.T) {
	t.Run("two streams", func(t *testing.T) {
		a := fromSlice([]int{1, 2})
		b := fromSlice([]int{3, 4, 5})
		out := Concat(a, b)
		got := collect(out)
		want := []int{1, 2, 3, 4, 5}
		if !equal(got, want) {
			t.Fatalf("Concat: got %v, want %v", got, want)
		}
	})

	t.Run("three streams", func(t *testing.T) {
		a := fromSlice([]int{1})
		b := fromSlice([]int{2})
		c := fromSlice([]int{3})
		out := Concat(a, b, c)
		got := collect(out)
		want := []int{1, 2, 3}
		if !equal(got, want) {
			t.Fatalf("Concat 3: got %v, want %v", got, want)
		}
	})

	t.Run("empty first", func(t *testing.T) {
		a := kernel.NewAsyncIterator[int](10)
		a.Close()
		b := fromSlice([]int{1, 2})
		out := Concat(a, b)
		got := collect(out)
		want := []int{1, 2}
		if !equal(got, want) {
			t.Fatalf("Concat empty first: got %v, want %v", got, want)
		}
	})

	t.Run("all empty", func(t *testing.T) {
		a, b := kernel.NewAsyncIterator[int](10), kernel.NewAsyncIterator[int](10)
		a.Close()
		b.Close()
		out := Concat(a, b)
		got := collect(out)
		if len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})
}

// ──────────────────────────────────────────────
// Merge
// ──────────────────────────────────────────────

func TestMerge(t *testing.T) {
	t.Run("two streams", func(t *testing.T) {
		ctx := context.Background()
		a := fromSlice([]int{1, 3, 5})
		b := fromSlice([]int{2, 4, 6})
		out := Merge(ctx, a, b)
		got := collect(out)
		// Order is non-deterministic but all 6 values must be present
		if len(got) != 6 {
			t.Fatalf("Merge: expected 6 elements, got %d: %v", len(got), got)
		}
		// Check all values are present (order-independent)
		seen := make(map[int]bool)
		for _, v := range got {
			seen[v] = true
		}
		for _, v := range []int{1, 2, 3, 4, 5, 6} {
			if !seen[v] {
				t.Fatalf("Merge: missing %d", v)
			}
		}
	})

	t.Run("empty streams", func(t *testing.T) {
		ctx := context.Background()
		a := kernel.NewAsyncIterator[int](10)
		a.Close()
		out := Merge(ctx, a)
		got := collect(out)
		if len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})

	t.Run("no streams", func(t *testing.T) {
		ctx := context.Background()
		out := Merge[int](ctx)
		got := collect(out)
		if len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})

	t.Run("one stream", func(t *testing.T) {
		ctx := context.Background()
		src := fromSlice([]int{10, 20, 30})
		out := Merge(ctx, src)
		got := collect(out)
		want := []int{10, 20, 30}
		if len(got) != 3 || got[0] != 10 || got[1] != 20 || got[2] != 30 {
			t.Fatalf("Merge one: got %v, want %v", got, want)
		}
	})
}

// ──────────────────────────────────────────────
// Buffer
// ──────────────────────────────────────────────

func TestBuffer(t *testing.T) {
	t.Run("exact size", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3, 4})
		out := Buffer(src, 2)
		got := collect(out)
		if len(got) != 2 {
			t.Fatalf("Buffer: expected 2 batches, got %d: %v", len(got), got)
		}
		if !equal(got[0], []int{1, 2}) || !equal(got[1], []int{3, 4}) {
			t.Fatalf("Buffer: unexpected batches: %v", got)
		}
	})

	t.Run("partial last batch", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3, 4, 5})
		out := Buffer(src, 3)
		got := collect(out)
		if len(got) != 2 {
			t.Fatalf("Buffer: expected 2 batches, got %d: %v", len(got), got)
		}
		if !equal(got[0], []int{1, 2, 3}) || !equal(got[1], []int{4, 5}) {
			t.Fatalf("Buffer partial: unexpected batches: %v", got)
		}
	})

	t.Run("size 1", func(t *testing.T) {
		src := fromSlice([]int{1, 2, 3})
		out := Buffer(src, 1)
		got := collect(out)
		if len(got) != 3 {
			t.Fatalf("Buffer size=1: expected 3 batches, got %d", len(got))
		}
	})

	t.Run("empty stream", func(t *testing.T) {
		src := kernel.NewAsyncIterator[int](10)
		src.Close()
		out := Buffer(src, 5)
		got := collect(out)
		if len(got) != 0 {
			t.Fatalf("expected empty, got %v", got)
		}
	})
}

// ──────────────────────────────────────────────
// Debounce
// ──────────────────────────────────────────────

func TestDebounce(t *testing.T) {
	t.Run("single value passes", func(t *testing.T) {
		src := fromSlice([]int{42})
		out := Debounce(src, 10*time.Millisecond)
		got := collect(out)
		if !equal(got, []int{42}) {
			t.Fatalf("Debounce single: got %v", got)
		}
	})

	t.Run("rapid values debounced to last", func(t *testing.T) {
		it := kernel.NewAsyncIterator[int](100)
		out := Debounce(it, 50*time.Millisecond)

		// Send rapidly then close
		go func() {
			for _, v := range []int{1, 2, 3, 4, 5} {
				it.Send(v)
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond)
			it.Close()
		}()

		got := collect(out)
		if len(got) == 0 {
			t.Fatal("Debounce: expected at least 1 value")
		}
		// Last value should be 5
		last := got[len(got)-1]
		if last != 5 {
			t.Fatalf("Debounce: expected last=5, got %d", last)
		}
	})
}

// ──────────────────────────────────────────────
// integration: pipeline composition
// ──────────────────────────────────────────────

func TestPipeline(t *testing.T) {
	// Map -> Filter -> Buffer
	src := fromSlice([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	mapped := Map(src, func(v int) int { return v * 2 })
	filtered := Filter(mapped, func(v int) bool { return v%4 == 0 }) // 4, 8, 12, 16, 20
	buffered := Buffer(filtered, 2)

	got := collect(buffered)

	if len(got) != 3 {
		t.Fatalf("Pipeline: expected 3 batches, got %d: %v", len(got), got)
	}
	// Batches: 4,8,12 (first two 4s - only 4), [8,12], [16,20]
	want := [][]int{{4, 8}, {12, 16}, {20}}
	for i, batch := range got {
		if !equal(batch, want[i]) {
			t.Fatalf("Pipeline batch %d: got %v, want %v", i, batch, want[i])
		}
	}
}

func TestMapReduce(t *testing.T) {
	// Map strings to lengths, then sum
	src := fromSlice([]string{"hello", "world", "gocel"})
	lengths := Map(src, func(v string) int { return len(v) })
	sum := Reduce(lengths, 0, func(acc, v int) int { return acc + v })
	if sum != 15 {
		t.Fatalf("MapReduce: got %d, want 15", sum)
	}
}

func TestConcatMap(t *testing.T) {
	a := fromSlice([]int{1, 2})
	b := fromSlice([]int{3, 4})
	c := Concat(a, b)
	doubled := Map(c, func(v int) int { return v * 2 })
	got := collect(doubled)
	want := []int{2, 4, 6, 8}
	if !equal(got, want) {
		t.Fatalf("ConcatMap: got %v, want %v", got, want)
	}
}

// ──────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
