package modelrouter

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ctxStreamModel records the stream's context so tests can observe when it
// is canceled, and returns readers that fail once the context is done —
// mirroring real providers, whose streams die with their ctx.
type ctxStreamModel struct {
	name   string
	block  bool // block in Stream until ctx.Done
	opened chan context.Context
	tokens []*types.Message
}

func (m *ctxStreamModel) Stream(ctx context.Context, _ []*types.Message, _ ...kernel.GenOption) (kernel.StreamReader, error) {
	if m.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if m.opened != nil {
		select {
		case m.opened <- ctx:
		default:
		}
	}
	return &ctxSliceReader{ctx: ctx, msgs: m.tokens, done: make(chan struct{})}, nil
}

func (m *ctxStreamModel) Generate(context.Context, []*types.Message, ...kernel.GenOption) (*types.Message, *types.TokenUsage, error) {
	return nil, nil, errors.New("not used")
}

func (m *ctxStreamModel) CountTokens(context.Context, []*types.Message, ...kernel.GenOption) (int, error) {
	return 0, nil
}

// ctxSliceReader serves a fixed token list and fails as soon as its context
// is done.
type ctxSliceReader struct {
	ctx  context.Context
	mu   sync.Mutex
	msgs []*types.Message
	done chan struct{}
	once sync.Once
}

func (r *ctxSliceReader) Recv() (*types.Message, error) {
	select {
	case <-r.ctx.Done():
		return nil, r.ctx.Err()
	default:
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.msgs) == 0 {
		return nil, io.EOF
	}
	m := r.msgs[0]
	r.msgs = r.msgs[1:]
	return m, nil
}

func (r *ctxSliceReader) Close() error {
	r.once.Do(func() { close(r.done) })
	return nil
}

func (r *ctxSliceReader) Done() <-chan struct{} { return r.done }

// TestStreamTimeout_KeepsDeliveredStreamAlive is the first C1 regression:
// the per-route timeout must bound stream OPENING, not kill the stream the
// moment it is delivered to the caller.
func TestStreamTimeout_KeepsDeliveredStreamAlive(t *testing.T) {
	model := &ctxStreamModel{
		name:   "m",
		opened: make(chan context.Context, 1),
		tokens: []*types.Message{types.NewAssistantMessage("tok")},
	}
	router := NewModelRouter([]ModelRoute{{Model: model, Timeout: 5000}})

	s, err := router.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream = %v", err)
	}
	msg, err := s.Recv()
	if err != nil || msg == nil || msg.Content != "tok" {
		t.Fatalf("Recv = %v, %v; want tok, nil (delivered stream was killed — C1)", msg, err)
	}
	s.Close()
}

// TestStreamRaceAll_WinnerSurvives is the second C1 regression: canceling
// the losing routes must not cancel the winning stream.
func TestStreamRaceAll_WinnerSurvives(t *testing.T) {
	fast := &ctxStreamModel{name: "fast", tokens: []*types.Message{types.NewAssistantMessage("fast-tok")}}
	slow := &ctxStreamModel{name: "slow", block: true}
	router := NewModelRouter(
		[]ModelRoute{{Model: fast}, {Model: slow}},
		WithRouterStrategy(StrategyRaceAll),
	)

	s, err := router.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream = %v", err)
	}
	msg, err := s.Recv()
	if err != nil || msg == nil || msg.Content != "fast-tok" {
		t.Fatalf("Recv = %v, %v; want fast-tok, nil (winner killed by race cancel — C1)", msg, err)
	}
	s.Close()
}
