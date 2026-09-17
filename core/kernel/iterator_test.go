package kernel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNewAsyncIterator_Basic(t *testing.T) {
	_ = NewAsyncIterator[int](10)
}

func TestAsyncIteratorDefaultBuffer(t *testing.T) {
	it := NewAsyncIterator[int](0)
	if cap(it.ch) != 100 {
		t.Errorf("buffer = %d, want 100", cap(it.ch))
	}
}

func TestAsyncIterator_SendAndNext(t *testing.T) {
	it, gen := NewAsyncIteratorPair[int]()
	go func() {
		gen.Send(1)
		gen.Send(2)
		gen.Send(3)
		gen.Close()
	}()
	var got []int
	for {
		v, ok := it.Next()
		if !ok {
			break
		}
		got = append(got, v)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("got %v, want [1 2 3]", got)
	}
}

func TestAsyncIterator_SendAfterClose(t *testing.T) {
	_, gen := NewAsyncIteratorPair[int]()
	gen.Close()
	ok := gen.Send(42)
	if ok {
		t.Error("send after close should return false")
	}
}

func TestAsyncIterator_Collect(t *testing.T) {
	it, gen := NewAsyncIteratorPair[int]()
	go func() {
		gen.Send(10)
		gen.Send(20)
		gen.Close()
	}()
	got := it.Collect()
	if len(got) != 2 || got[0] != 10 || got[1] != 20 {
		t.Errorf("got %v, want [10 20]", got)
	}
}

func TestAsyncIterator_CloseIdempotent(t *testing.T) {
	_, gen := NewAsyncIteratorPair[int]()
	gen.Close()
	gen.Close()
}

func TestAsyncIterator_Err(t *testing.T) {
	it, gen := NewAsyncIteratorPair[int]()
	sentErr := errors.New("test error")
	gen.CloseWithError(sentErr)
	if it.Err() != sentErr {
		t.Errorf("Err = %v, want %v", it.Err(), sentErr)
	}
}

func TestAsyncIterator_Done(t *testing.T) {
	it, gen := NewAsyncIteratorPair[int]()
	gen.Close()
	select {
	case <-it.Done():
	case <-time.After(time.Second):
		t.Fatal("Done channel not closed")
	}
}

func TestAsyncIterator_Channel(t *testing.T) {
	it, gen := NewAsyncIteratorPair[string]()
	go func() {
		gen.Send("a")
		gen.Send("b")
		gen.Close()
	}()
	ch := it.Channel()
	vals := make([]string, 0, 2)
	for v := range ch {
		vals = append(vals, v)
	}
	if len(vals) != 2 {
		t.Errorf("got %d values from channel", len(vals))
	}
}

func TestAsyncIterator_Ch(t *testing.T) {
	it, gen := NewAsyncIteratorPair[int]()
	gen.Send(1)
	gen.Close()
	ch := it.Ch()
	v, ok := <-ch
	if !ok {
		t.Fatal("channel closed prematurely")
	}
	if v != 1 {
		t.Errorf("got %d, want 1", v)
	}
}

func TestNewAsyncIteratorWithContext_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	it, gen := NewAsyncIteratorPairWithContext[int](ctx)

	go func() {
		gen.Send(1)
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	v, ok := it.Next()
	if !ok || v != 1 {
		t.Errorf("first value: got %d, %v", v, ok)
	}
	_, ok = it.Next()
	if ok {
		t.Error("should not get value after cancel")
	}
	if it.Err() == nil {
		t.Error("Err should be non-nil after cancel")
	}
}

func TestIterate(t *testing.T) {
	ctx := context.Background()
	it := Iterate(ctx, 10, func(ctx context.Context, ch chan<- int) error {
		ch <- 1
		ch <- 2
		return nil
	})
	vals := it.Collect()
	if len(vals) != 2 || vals[0] != 1 || vals[1] != 2 {
		t.Errorf("got %v, want [1 2]", vals)
	}
}

func TestIterate_Error(t *testing.T) {
	ctx := context.Background()
	expectedErr := errors.New("iteration error")
	it := Iterate(ctx, 10, func(ctx context.Context, ch chan<- int) error {
		return expectedErr
	})
	it.Collect()
	if it.Err() != expectedErr {
		t.Errorf("Err = %v, want %v", it.Err(), expectedErr)
	}
}

func TestAsyncIterator_Concurrency(t *testing.T) {
	it, gen := NewAsyncIteratorPair[int]()
	var wg sync.WaitGroup
	const count = 50
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < count; i++ {
			gen.Send(i)
		}
		gen.Close()
	}()
	received := make([]bool, count)
	for {
		v, ok := it.Next()
		if !ok {
			break
		}
		if v >= 0 && v < count {
			received[v] = true
		}
	}
	wg.Wait()
	for i, seen := range received {
		if !seen {
			t.Errorf("value %d not received", i)
		}
	}
}

func TestAsyncGenerator_CloseWithError(t *testing.T) {
	it, gen := NewAsyncIteratorPair[int]()
	testErr := errors.New("generator error")
	gen.CloseWithError(testErr)

	if it.Err() != testErr {
		t.Errorf("Err = %v, want %v", it.Err(), testErr)
	}
	_, ok := it.Next()
	if ok {
		t.Error("should not get value after error close")
	}
}
