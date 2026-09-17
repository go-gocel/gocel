package graph

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ── InterruptHandle ────────────────────────────────────────────

// InterruptHandle 提供对中断节点的外部控制。
// 可以通过 InjectInput 从外部（如 HTTP handler）注入恢复信号。
//
// InterruptHandle provides external control over an interrupt node.
// Use InjectInput to send a resume signal from outside (e.g., an HTTP handler).
type InterruptHandle struct {
	mu      sync.Mutex
	inputCh chan string
	closed  bool
	checkID string
}

// InjectInput 向中断节点注入恢复信号。
// 信号可以是分支标签（如 "continue"、"human_input"），
// 或特殊值 "stop" 表示取消执行。
//
// InjectInput sends a resume signal to the interrupt node.
// The signal can be a branch label (e.g. "continue", "human_input"),
// or the special value "stop" to cancel execution.
func (h *InterruptHandle) InjectInput(input string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return fmt.Errorf("interrupt handle is closed")
	}
	select {
	case h.inputCh <- input:
		return nil
	default:
		return fmt.Errorf("interrupt handle buffer is full")
	}
}

// CheckpointID 返回最近保存的 checkpoint ID（如果有）。
//
// CheckpointID returns the most recent checkpoint ID, if any.
func (h *InterruptHandle) CheckpointID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.checkID
}

// Close 关闭中断句柄，释放通道。
//
// Close closes the interrupt handle and releases the channel.
func (h *InterruptHandle) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		close(h.inputCh)
		h.closed = true
	}
}

// ── InterruptConfig ────────────────────────────────────────────

// InterruptConfig 配置中断节点的行为。
//
// InterruptConfig configures the interrupt node.
type InterruptConfig struct {
	// CheckpointStore 用于保存中断时的状态快照。
	// 如果为 nil，则不保存 checkpoint，仅暂停。
	CheckpointStore kernel.CheckpointStore

	// Timeout 是等待中断输入的超时时间。
	// 如果为 0，则无限等待。
	Timeout time.Duration
}

// InterruptOption 配置中断节点。
//
// InterruptOption configures an interrupt node.
type InterruptOption func(*InterruptConfig)

// WithCheckpointStore 设置 checkpoint 存储。
//
// WithCheckpointStore sets the checkpoint store.
func WithCheckpointStore(cs kernel.CheckpointStore) InterruptOption {
	return func(c *InterruptConfig) {
		c.CheckpointStore = cs
	}
}

// WithInterruptTimeout 设置中断等待超时时间。
// 0 表示无限等待。
//
// WithInterruptTimeout sets the interrupt timeout. 0 means wait indefinitely.
func WithInterruptTimeout(d time.Duration) InterruptOption {
	return func(c *InterruptConfig) {
		c.Timeout = d
	}
}

// ── NewInterruptNode ──────────────────────────────────────────

// NewInterruptNode 创建一个中断变换函数和对应的外部控制句柄。
// 用法：graph.AddPassthroughNode("human_approval", NewInterruptNode(cs))
//
// 图中执行到此节点时：
//  1. 保存当前 GraphState 到 checkpoint（如果配置了 CheckpointStore）
//  2. 发送 EventInterrupt 事件
//  3. 阻塞等待恢复信号
//  4. 恢复执行
//
// NewInterruptNode creates an interrupt transform function and a handle.
func NewInterruptNode(cfg *InterruptConfig) (TransformFn, *InterruptHandle) {
	if cfg == nil {
		cfg = &InterruptConfig{}
	}

	inputCh := make(chan string, 1)
	handle := &InterruptHandle{
		inputCh: inputCh,
	}

	fn := func(ctx ContextWithState, state *GraphState) error {
		// Prevent recursive interrupts
		if fromCtx := ctx.Value(interruptInProgKey{}); fromCtx != nil {
			return nil
		}
		ctx = context.WithValue(ctx, interruptInProgKey{}, true)

		// Get node ID from state context
		nodeID := "interrupt"
		if nid, ok := state.Get("__node_id"); ok {
			if s, ok := nid.(string); ok {
				nodeID = s
			}
		}

		// Execution flow: save checkpoint → emit interrupt event → wait for resume signal
		// 执行流程：保存 checkpoint → 发送中断事件 → 等待恢复信号
		saveCheckpoint(ctx, state, cfg, handle, nodeID)
		emitInterruptEvent(ctx, state, nodeID)
		input, err := waitForResume(ctx, inputCh, cfg, nodeID)
		if err != nil {
			return err
		}
		// The resume signal IS the branch label: record it in state so the
		// scheduler routes the node's labeled out-edges (approve/reject).
		// "stop"/"cancel" already surfaced as errors upstream.
		if input != "" {
			state.SetWithSource("branch", input, nodeID)
		}
		return nil
	}

	return fn, handle
}

// ── Helper functions ──────────────────────────────────────────

// saveCheckpoint saves the current graph state as a checkpoint if a CheckpointStore is configured.
// On success, it updates the handle's checkpoint ID for external retrieval.
//
// saveCheckpoint 在配置了 CheckpointStore 时将当前图状态保存为 checkpoint。
// 保存成功后更新句柄中的 checkpoint ID，供外部获取。
func saveCheckpoint(ctx context.Context, state *GraphState, cfg *InterruptConfig, handle *InterruptHandle, nodeID string) {
	if cfg.CheckpointStore == nil {
		return
	}
	cpID := fmt.Sprintf("int_%s_%d", nodeID, time.Now().UnixNano())

	// Extract messages from state
	var msgs []*types.Message
	if v, ok := state.Get("messages"); ok {
		if m, ok := v.([]*types.Message); ok {
			msgs = types.CloneMessages(m)
		}
	}
	if msgs == nil {
		msgs = []*types.Message{}
	}

	cp := &types.Checkpoint{
		ID:        cpID,
		AgentName: nodeID,
		Messages:  msgs,
		StepIndex: -1,
		CreatedAt: time.Now(),
		Meta: map[string]any{
			"graph_interrupt": true,
			"interrupt_node":  nodeID,
		},
	}
	if err := cfg.CheckpointStore.Save(ctx, cp); err == nil {
		handle.mu.Lock()
		handle.checkID = cpID
		handle.mu.Unlock()
	}
}

// emitInterruptEvent sends an EventInterrupt event via the AgentContext (if available).
//
// emitInterruptEvent 通过 AgentContext（如果可用）发送 EventInterrupt 事件。
func emitInterruptEvent(ctx context.Context, state *GraphState, nodeID string) {
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		if sendEvent := ac.SendEvent(); sendEvent != nil {
			sendEvent(&types.Event{
				Type:    types.EventInterrupt,
				Content: "graph_interrupt",
				Meta: map[string]any{
					"node_id":     nodeID,
					"node_kind":   "interrupt",
					"state_count": len(state.Snapshot()),
				},
			})
		}
	}
}

// waitForResume blocks until a resume signal arrives, a timeout occurs, or
// the context is cancelled. It returns the injected input (the branch
// label) on resume; "stop"/"cancel" and a CLOSED handle are errors — a
// closed handle must never silently resume the interrupted node.
//
// waitForResume 阻塞等待恢复信号、超时或上下文取消。恢复时返回注入的
// 输入（分支标签）；"stop"/"cancel" 与句柄关闭都是错误——句柄关闭绝不
// 能静默恢复被中断的节点。
func waitForResume(ctx context.Context, inputCh chan string, cfg *InterruptConfig, nodeID string) (string, error) {
	// A buffered relay decouples the (possibly closed) input channel from
	// the timeout select — the relay goroutine never leaks (buffer 1).
	relay := make(chan string, 1)
	go func() {
		input, ok := <-inputCh
		if !ok {
			close(relay)
			return
		}
		relay <- input
	}()

	handle := func(input string, ok bool) (string, error) {
		if !ok {
			return "", fmt.Errorf("interrupt %s: handle closed", nodeID)
		}
		if input == "stop" || input == "cancel" {
			return "", fmt.Errorf("interrupt %s: cancelled by user", nodeID)
		}
		return input, nil
	}

	if cfg.Timeout > 0 {
		timer := time.NewTimer(cfg.Timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			return "", fmt.Errorf("interrupt %s: timeout after %v", nodeID, cfg.Timeout)
		case <-ctx.Done():
			return "", ctx.Err()
		case input, ok := <-relay:
			return handle(input, ok)
		}
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case input, ok := <-relay:
		return handle(input, ok)
	}
}

// interruptInProgKey prevents recursive interrupt handling.
type interruptInProgKey struct{}
