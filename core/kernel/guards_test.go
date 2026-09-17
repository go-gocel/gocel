package kernel_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
)

// Regression tests for latent contract-layer defects found in the deep
// functional audit (C-series): WithDetail nil-map panic, missing sentinel
// unwrapping, NormalizeToolCall nil deref, and cancellation classified as
// retryable.

func TestWithDetail_NoPanicOnNilDetails(t *testing.T) {
	// Both Details fields are exported, so hand-built structs can carry a
	// nil map — WithDetail must not panic on them.
	nf := &kernel.NotFoundError{Kind: "tool", Key: "x"}
	if got := nf.WithDetail("k", "v"); got.Details["k"] != "v" {
		t.Fatalf("NotFoundError.WithDetail: detail not recorded")
	}
	ae := &kernel.AgentError{Code: kernel.CodeToolError, Agent: "a"}
	if got := ae.WithDetail("k", "v"); got.Details["k"] != "v" {
		t.Fatalf("AgentError.WithDetail: detail not recorded")
	}
}

func TestCircuitError_UnwrapsSentinel(t *testing.T) {
	// The typed error must classify via errors.Is like the bare sentinel.
	if !errors.Is(&kernel.CircuitError{Route: "r"}, kernel.ErrCircuitOpen) {
		t.Fatalf("CircuitError does not unwrap to ErrCircuitOpen")
	}
}

func TestRateLimitError_UnwrapsSentinel(t *testing.T) {
	if !errors.Is(&kernel.RateLimitError{Route: "r", RetryAfter: 1}, kernel.ErrRateLimited) {
		t.Fatalf("RateLimitError does not unwrap to ErrRateLimited")
	}
}

func TestNormalizeToolCall_NilSafe(t *testing.T) {
	// Public mutating API must not panic on nil.
	kernel.NormalizeToolCall(nil)
}

func TestIsRetryableError_CancellationNotRetryable(t *testing.T) {
	// Cancellation is a caller decision, never a retry trigger — even when
	// wrapped by a provider.
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("wrap: %w", context.Canceled),
		fmt.Errorf("wrap: %w", context.DeadlineExceeded),
	} {
		if kernel.IsRetryableError(err) {
			t.Fatalf("IsRetryableError(%v) = true, want false", err)
		}
	}
}
