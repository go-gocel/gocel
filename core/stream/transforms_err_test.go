package stream

import (
	"errors"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
)

// TestMapErr_RecordsError (A3): the documented contract says the first fn
// error closes the output iterator WITH that error — Err() must return it
// instead of staying nil forever.
func TestMapErr_RecordsError(t *testing.T) {
	src := kernel.NewAsyncIterator[int](4)
	want := errors.New("transform boom")
	dst := MapErr(src, func(v int) (int, error) {
		if v == 2 {
			return 0, want
		}
		return v * 10, nil
	})
	src.Send(1)
	src.Send(2)
	src.Close()

	// Drain until closed.
	for {
		if _, ok := dst.Next(); !ok {
			break
		}
	}
	if !errors.Is(dst.Err(), want) {
		t.Fatalf("Err() = %v, want the transform error", dst.Err())
	}
}

// TestMapErr_PropagatesSourceError: when the SOURCE closed with an error,
// the output must carry it too (the package contract says transforms
// propagate source errors).
func TestMapErr_PropagatesSourceError(t *testing.T) {
	src := kernel.NewAsyncIterator[int](4)
	want := errors.New("source boom")
	inner := MapErr(src, func(v int) (int, error) { return v, nil })
	src.Send(1)
	src.CloseWithError(want) // see helper below
	dst := Map(inner, func(v int) int { return v * 2 })
	for {
		if _, ok := dst.Next(); !ok {
			break
		}
	}
	if !errors.Is(dst.Err(), want) {
		t.Fatalf("Err() = %v, want the source error", dst.Err())
	}
}

// TestFilter_PropagatesSourceError covers the other chained transform.
func TestFilter_PropagatesSourceError(t *testing.T) {
	src := kernel.NewAsyncIterator[int](4)
	want := errors.New("upstream boom")
	inner := MapErr(src, func(v int) (int, error) { return v, nil })
	src.Send(1)
	src.CloseWithError(want)
	dst := Filter(inner, func(v int) bool { return v > 0 })
	for {
		if _, ok := dst.Next(); !ok {
			break
		}
	}
	if !errors.Is(dst.Err(), want) {
		t.Fatalf("Err() = %v, want the source error", dst.Err())
	}
}
