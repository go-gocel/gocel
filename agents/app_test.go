package agents

import (
	"context"
	"errors"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runner"
	"github.com/go-gocel/gocel/core/types"
)

// echoModel answers "done" immediately with no tool calls.
type echoModel struct{}

func (echoModel) Generate(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return types.NewAssistantMessage("done"), &types.TokenUsage{TotalTokens: 1}, nil
}

func (echoModel) Stream(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (kernel.StreamReader, error) {
	return nil, errors.New("stream not mocked")
}

func (echoModel) CountTokens(_ context.Context, _ []*types.Message, _ ...kernel.GenOption) (int, error) {
	return 0, nil
}

func TestNew_BasicAgent(t *testing.T) {
	a := New(WithName("my-agent"), WithSystemPrompt("be helpful"))
	if a.Name() != "my-agent" {
		t.Fatalf("Name = %q, want my-agent", a.Name())
	}

	r := runner.NewRunner(a, echoModel{})
	info := r.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	if info.Err != nil {
		t.Fatalf("Run: %v", info.Err)
	}
	if info.Result == nil || info.Result.Content != "done" {
		t.Fatalf("result = %+v, want content done", info.Result)
	}
}

func TestNewReactAgent_Defaults(t *testing.T) {
	a := NewReactAgent()
	if a.Name() != "react-agent" {
		t.Fatalf("Name = %q, want react-agent", a.Name())
	}

	r := runner.NewRunner(a, echoModel{})
	info := r.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("hi")},
	})
	if info.Err != nil {
		t.Fatalf("Run: %v", info.Err)
	}
	if info.Result == nil || info.Result.Content != "done" {
		t.Fatalf("result = %+v, want content done", info.Result)
	}
}

func TestNewReactAgent_OverridesPrompt(t *testing.T) {
	a := NewReactAgent(WithSystemPrompt("custom prompt"))
	if a.Description() != "" {
		t.Fatalf("Description = %q, want empty", a.Description())
	}
	_ = a
}
