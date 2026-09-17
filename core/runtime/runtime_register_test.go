package runtime

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
)

// emptyProvider lists zero tools — legal (an MCP server exposing nothing).
type emptyProvider struct{}

func (emptyProvider) Name() string { return "empty" }
func (emptyProvider) ListTools(context.Context) ([]kernel.Tool, error) {
	return []kernel.Tool{}, nil
}

// TestRegister_EmptyProviderDoesNotPanic is the C7 regression: an empty tool
// list must be a no-op registration, never a panic.
func TestRegister_EmptyProviderDoesNotPanic(t *testing.T) {
	rt := NewRuntime(nil, tool.NewMapToolRegistry(nil))
	if err := rt.Register(context.Background(), emptyProvider{}); err != nil {
		t.Fatalf("Register(empty provider) = %v, want nil", err)
	}
	if got := rt.ListTools(context.Background()); len(got) != 0 {
		t.Fatalf("tools after empty register = %d, want 0", len(got))
	}
}
