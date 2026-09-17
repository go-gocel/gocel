package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
)

// Regression: a panicking module hook must surface as an error, never
// crash the host process (C3).
func TestFireStepStart_HookPanicBecomesError(t *testing.T) {
	rt := NewRuntime(nil, nil)
	rt.OnStepStart(func(context.Context, *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
		panic("hook bug")
	})
	_, cont, _, err := rt.FireStepStart(context.Background(), &kernel.StepInfo{StepIndex: 0})
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("FireStepStart = (cont=%v, err=%v), want a panic error", cont, err)
	}
}
