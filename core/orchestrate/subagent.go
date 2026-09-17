package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// SubagentStatus is the lifecycle phase of a child agent session.
// SubagentStatus 是子代理会话的生命周期阶段。
type SubagentStatus string

const (
	// StatusRunning marks a session whose turn is executing.
	// StatusRunning 表示会话正在执行一轮。
	StatusRunning SubagentStatus = "running"
	// StatusIdle marks a session between turns; the session is continuable.
	// StatusIdle 表示会话处于轮次之间，可继续续传。
	StatusIdle SubagentStatus = "idle"
	// StatusCompleted marks a one-shot session that finished its single turn; it is terminal.
	// StatusCompleted 表示一次性会话已完成其唯一一轮，处于终态。
	StatusCompleted SubagentStatus = "completed"
	// StatusFailed marks a session whose turn errored; it is terminal (notice sent).
	// StatusFailed 表示会话某轮出错，处于终态（已发送通知）。
	StatusFailed SubagentStatus = "failed"
	// StatusDisposed marks a session ended by its parent; it is terminal (notice sent).
	// StatusDisposed 表示父级已结束会话，处于终态（已发送通知）。
	StatusDisposed SubagentStatus = "disposed"
)

// Subagent is the public projection of one child session.
// Subagent 是单个子会话的对外投影。
type Subagent struct {
	ID       string         `json:"id"`
	Label    string         `json:"label,omitempty"`
	ParentID string         `json:"parent_id,omitempty"`
	Status   SubagentStatus `json:"status"`
}

var (
	// ErrSubagentNotFound reports that the given id is unknown.
	// ErrSubagentNotFound 表示给定的 id 未知。
	ErrSubagentNotFound = errors.New("orchestrate: subagent not found")
	// ErrSubagentNotContinuable reports that the session settled (failed/disposed).
	// ErrSubagentNotContinuable 表示会话已了结（failed/disposed），不可续传。
	ErrSubagentNotContinuable = errors.New("orchestrate: subagent is not continuable")
)

// Registry manages child-agent sessions. List shows only live sessions
// (running/idle); settled sessions stay internally queryable through Wait
// until Dispose removes them. The factory-built agent is the sole creator
// and the handle id is the only capability to continue or interrupt —
// mirroring DSH's subagent registry semantics.
//
// Registry 管理子代理会话。List 只显示活体会话（running/idle）；已了结
// 的会话保留在内部供 Wait 查询，直到 Dispose 移除。工厂构建的 Agent 是
// 唯一创造者，句柄 id 是续传/中断的唯一能力——对应 DSH 语义。
type Registry struct {
	mu    sync.Mutex
	seq   int
	items map[string]*subagentEntry
}

// NewRegistry creates an empty registry.
// NewRegistry 创建一个空的注册表。
func NewRegistry() *Registry {
	return &Registry{items: make(map[string]*subagentEntry)}
}

type subagentEntry struct {
	sub   Subagent
	agent kernel.Agent
	rt    kernel.Runtime
	send  func(*types.Event) bool
	runCh chan string

	// delegatedApproval is the approval policy pinned at the delegation
	// boundary (DSH delegation policy inheritance): captured from the
	// parent context at spawn, injected into every child turn. A nil value
	// means the parent pinned nothing — the child resolves its approval
	// disposition from its own runtime configuration.
	//
	// delegatedApproval 是委派边界钉住的审批策略（DSH 委派策略继承）：
	// spawn 时从父上下文捕获，注入子代理的每一轮。nil 表示父级未钉——
	// 子代理按自身运行时配置解析审批处置。
	delegatedApproval kernel.ApprovalPolicy

	// oneShot sessions run exactly one turn and then settle (DSH one-shot
	// subagent provider: workflow fan-out and other fire-and-await callers).
	oneShot bool

	// settleMu serializes status transitions against Continue sends and
	// Dispose's channel close (no send-on-closed-channel, no lost state).
	settleMu sync.Mutex
	status   SubagentStatus
	result   *kernel.Result
	disposed bool

	// cancelMu guards the current turn's cancel + the interrupt flag.
	cancelMu  sync.Mutex
	cancel    context.CancelFunc
	interrupt bool

	done chan struct{}
	once sync.Once
}

func (r *Registry) nextID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return fmt.Sprintf("sub-%d", r.seq)
}

func (r *Registry) lookup(id string) (*subagentEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.items[id]
	if !ok {
		return nil, ErrSubagentNotFound
	}
	return e, nil
}

// Spawn starts a background child session: agent runs the initial input in
// its own goroutine with the parent's runtime (model + tools), and every
// turn is continuable through Continue. Completion notices travel through
// the parent run's SendEvent. ctx is only used for the initial snapshot of
// the parent context; the session itself outlives it.
//
// Spawn 启动后台子会话：agent 用父运行时（模型+工具）在独立 goroutine
// 中执行初始输入，每轮之后可经 Continue 续传。完成通知经父运行的
// SendEvent 送达。ctx 仅用于父上下文快照；会话本身比它活得久。
func (r *Registry) Spawn(ctx context.Context, parentID, label string, agent kernel.Agent, input *types.AgentInput) (*Subagent, error) {
	e, err := r.spawn(ctx, parentID, label, agent, input, false)
	if err != nil {
		return nil, err
	}
	return &e.sub, nil
}

// Run spawns a one-shot child: exactly one turn, then the session settles
// as completed (or failed) and the registry entry is removed. The returned
// Result is the child's final result — synchronous fire-and-await semantics
// on top of the same registry (DSH one-shot subagent provider). A canceled
// ctx disposes the child and returns the ctx error.
//
// Run 启动一次性子会话：恰好一轮，随后会话了结为 completed（或 failed），
// 注册表条目移除。返回的是子代理最终 Result——同一注册表上的同步
// fire-and-await 语义（DSH one-shot 子代理 provider）。ctx 取消则销毁
// 子会话并返回 ctx 错误。
func (r *Registry) Run(ctx context.Context, parentID, label string, agent kernel.Agent, input *types.AgentInput) (*kernel.Result, error) {
	e, err := r.spawn(ctx, parentID, label, agent, input, true)
	if err != nil {
		return nil, err
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		// Cancel the child and wait for its turn to stop; Dispose is safe
		// after settlement (the done channel is already closed).
		_ = r.Dispose(context.Background(), e.sub.ID)
		return nil, ctx.Err()
	}
	e.settleMu.Lock()
	result := e.result
	e.settleMu.Unlock()
	r.mu.Lock()
	delete(r.items, e.sub.ID)
	r.mu.Unlock()
	return result, nil
}

// spawn is the shared creation path for Spawn (continuable) and Run
// (one-shot).
func (r *Registry) spawn(ctx context.Context, parentID, label string, agent kernel.Agent, input *types.AgentInput, oneShot bool) (*subagentEntry, error) {
	if agent == nil {
		return nil, errors.New("orchestrate: nil subagent")
	}
	if input == nil {
		input = &types.AgentInput{}
	}
	rt := kernel.RuntimeFromContext(ctx)
	if rt == nil {
		return nil, errors.New("orchestrate: no runtime in context — subagents need the parent runtime")
	}
	var send func(*types.Event) bool
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		send = ac.SendEvent()
	}

	e := &subagentEntry{
		sub:               Subagent{ID: r.nextID(), Label: label, ParentID: parentID, Status: StatusRunning},
		agent:             agent,
		rt:                rt,
		send:              send,
		delegatedApproval: kernel.DelegatedApprovalFromContext(ctx),
		runCh:             make(chan string, 8),
		status:            StatusRunning,
		done:              make(chan struct{}),
		oneShot:           oneShot,
	}
	r.mu.Lock()
	r.items[e.sub.ID] = e
	r.mu.Unlock()

	first := input
	if len(input.Messages) == 0 {
		first = &types.AgentInput{Messages: []*types.Message{types.NewUserMessage("work")}}
	}

	go e.loop(first)
	return e, nil
}

// loop executes turns until the session is disposed or a turn fails.
func (e *subagentEntry) loop(initial *types.AgentInput) {
	defer e.once.Do(func() { close(e.done) })
	nextInput := initial
	for {
		e.settleMu.Lock()
		e.status = StatusRunning
		disposed := e.disposed
		e.settleMu.Unlock()
		if disposed {
			e.notify(StatusDisposed, "")
			return
		}

		rctx, cancel := context.WithCancel(context.Background())
		if e.delegatedApproval != nil {
			rctx = kernel.WithDelegatedApproval(rctx, e.delegatedApproval)
		}
		e.cancelMu.Lock()
		e.cancel = cancel
		e.cancelMu.Unlock()

		result := e.agent.Run(rctx, nextInput, e.rt)

		e.cancelMu.Lock()
		e.cancel = nil
		interrupted := e.interrupt
		e.interrupt = false
		e.cancelMu.Unlock()
		cancel()

		if result.Err != nil && !interrupted {
			e.settleMu.Lock()
			if e.disposed {
				e.settleMu.Unlock()
				e.notify(StatusDisposed, "")
				return
			}
			e.status = StatusFailed
			e.result = result
			e.settleMu.Unlock()
			e.notify(StatusFailed, result.Err.Error())
			return
		}

		e.settleMu.Lock()
		e.status = StatusIdle
		e.result = result
		e.settleMu.Unlock()

		if e.oneShot {
			// One-shot: the single turn is done — settle completed and end.
			e.settleMu.Lock()
			if e.disposed {
				e.settleMu.Unlock()
				e.notify(StatusDisposed, "")
				return
			}
			e.disposed = true
			e.status = StatusCompleted
			e.settleMu.Unlock()
			e.notify(StatusCompleted, "")
			return
		}

		select {
		case msg, ok := <-e.runCh:
			if !ok {
				e.notify(StatusDisposed, "")
				return
			}
			nextInput = &types.AgentInput{Messages: []*types.Message{types.NewUserMessage(msg)}}
		}
	}
}

// Continue queues a follow-up message for an idle or running session. The
// message runs after the current turn finishes (DSH send_message waits for
// the current turn).
//
// Continue 为 idle 或 running 会话排队一条后续消息；消息在当前轮结束后
// 执行（对应 DSH send_message 等待当前轮完成的语义）。
func (r *Registry) Continue(_ context.Context, id, message string) error {
	if message == "" {
		return errors.New("orchestrate: empty follow-up message")
	}
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	e.settleMu.Lock()
	defer e.settleMu.Unlock()
	if e.disposed || e.status == StatusFailed {
		return ErrSubagentNotContinuable
	}
	select {
	case e.runCh <- message:
		return nil
	case <-e.done:
		return ErrSubagentNotContinuable
	}
}

// Interrupt cancels the current turn only; the session stays continuable
// (DSH interrupt semantics — it never settles the child).
//
// Interrupt 只取消当前轮；会话仍可续传（DSH 中断语义——绝不了结子会话）。
func (r *Registry) Interrupt(_ context.Context, id string) error {
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	e.cancelMu.Lock()
	e.interrupt = true
	cancel := e.cancel
	e.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Dispose ends the session: the current turn is canceled, the session
// settles as disposed (notice sent), and the registry entry is removed.
//
// Dispose 结束会话：取消当前轮、会话以 disposed 了结（发送通知），并从注册表移除条目。
func (r *Registry) Dispose(_ context.Context, id string) error {
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	e.settleMu.Lock()
	e.disposed = true
	close(e.runCh)
	e.settleMu.Unlock()
	e.cancelMu.Lock()
	if e.cancel != nil {
		e.cancel()
	}
	e.cancelMu.Unlock()
	<-e.done
	r.mu.Lock()
	delete(r.items, id)
	r.mu.Unlock()
	return nil
}

// List returns the live sessions (running/idle). Settled sessions are not
// listed — the registry records live agents.
//
// List 返回活体会话（running/idle）。已了结的会话不会列出——注册表只记录活体代理。
func (r *Registry) List(_ context.Context) []*Subagent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Subagent
	for _, e := range r.items {
		e.settleMu.Lock()
		status := e.status
		e.settleMu.Unlock()
		if status == StatusFailed || status == StatusDisposed || status == StatusCompleted {
			continue
		}
		c := e.sub
		c.Status = status
		out = append(out, &c)
	}
	return out
}

// Descendants returns the live sessions under parentID (roots' children
// when parentID is empty).
//
// Descendants 返回 parentID 下的活体会话（parentID 为空时返回根的子会话）。
func (r *Registry) Descendants(_ context.Context, parentID string) []*Subagent {
	var out []*Subagent
	for _, s := range r.List(context.Background()) {
		if s.ParentID == parentID {
			out = append(out, s)
		}
	}
	return out
}

// Wait blocks until the session settles (failed/disposed) or ctx is done,
// returning the last result.
//
// Wait 阻塞直到会话了结（failed/disposed）或 ctx 结束，返回最后的结果。
func (r *Registry) Wait(ctx context.Context, id string) (*kernel.Result, error) {
	e, err := r.lookup(id)
	if err != nil {
		return nil, err
	}
	select {
	case <-e.done:
		e.settleMu.Lock()
		defer e.settleMu.Unlock()
		return e.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// notify sends the settlement notice through the parent run's event
// channel; a nil sender drops it.
func (e *subagentEntry) notify(status SubagentStatus, errText string) {
	if e.send == nil {
		return
	}
	e.send(types.NoticeEvent(&types.Notice{
		Kind:   types.NoticeKindSubagent,
		ID:     e.sub.ID,
		Status: string(status),
		Label:  e.sub.Label,
		Err:    errText,
		At:     time.Now(),
	}))
}
