package subagent_control

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

type holdAgent struct {
	block   chan struct{}
	started chan string
	turns   chan string
}

func (a *holdAgent) Name() string        { return "hold" }
func (a *holdAgent) Description() string { return "hold" }
func (a *holdAgent) Run(ctx context.Context, input *types.AgentInput, _ kernel.Runtime) *kernel.Result {
	content := ""
	if len(input.Messages) > 0 {
		content = input.Messages[0].Content
	}
	a.started <- content
	a.turns <- content
	select {
	case <-a.block:
	case <-ctx.Done():
		return &kernel.Result{Err: ctx.Err()}
	}
	return &kernel.Result{Content: "done"}
}

func ctrlCtx() context.Context {
	rt := runtime.NewRuntime(nil, nil)
	ac := runtime.NewAgentContext(runtime.WithContextFacts(&types.RuntimeFacts{SessionID: "s1"}))
	ctx := kernel.WithRuntime(context.Background(), rt)
	return kernel.WithAgentContext(ctx, ac)
}

func waitTurn(t *testing.T, ch chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("turn = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn never started")
	}
}

func TestControlTools_SendInterruptList(t *testing.T) {
	reg := orchestrate.NewRegistry()
	block := make(chan struct{})
	child := &holdAgent{block: block, started: make(chan string, 4), turns: make(chan string, 4)}
	sub, err := reg.Spawn(ctrlCtx(), "s1", "c", child, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage("first")},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitTurn(t, child.started, "first")

	ts, err := Tools(Config{Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	send, interrupt, list := ts[0], ts[1], ts[2]

	// list_agents shows the live child.
	out, err := list.Run(ctrlCtx(), "{}")
	if err != nil || !strings.Contains(out, sub.ID) {
		t.Fatalf("list_agents = %q, %v; want the child id", out, err)
	}

	// interrupt_agent stops the current turn only.
	if _, err := interrupt.Run(ctrlCtx(), `{"id":"`+sub.ID+`"}`); err != nil {
		t.Fatalf("interrupt_agent = %v", err)
	}

	// send_message queues the next turn (runs after the interrupted one).
	if _, err := send.Run(ctrlCtx(), `{"id":"`+sub.ID+`","message":"second"}`); err != nil {
		t.Fatalf("send_message = %v", err)
	}
	close(block)
	waitTurn(t, child.started, "second")
	<-child.turns
	<-child.turns
}
