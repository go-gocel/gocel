package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// AutoSaveOption configures the auto-save module.
//
// AutoSaveOption 配置自动保存模块。
type AutoSaveOption func(*autoSaveConfig)

type autoSaveConfig struct {
	interval  int
	sessionID string
	barriers  Barrier
}

// Barrier selects the semantic persistence barrier points (DSH
// session-checkpoint-policy): a barrier saves the checkpoint BEFORE the
// selected boundary and FAILS CLOSED when the save fails — the execution
// intent is durable before the side effect happens, and an undurable
// intent never proceeds.
//
// Barrier 选择语义持久化屏障点（DSH session-checkpoint-policy）：屏障在
// 所选边界之前保存检查点，保存失败即 fail-closed——副作用发生前执行意图
// 已耐久，不可久化的意图绝不推进。
type Barrier int

const (
	// BarrierModelCall saves before every model request: the messages that
	// produced this request are durable before the request is sent.
	//
	// BarrierModelCall 在每次模型请求前保存：产生该请求的消息在请求发出
	// 前已耐久。
	BarrierModelCall Barrier = 1 << iota
	// BarrierToolCall saves before every top-level tool side effect: the
	// intent to call the tool is durable before the tool runs.
	//
	// BarrierToolCall 在每次顶层工具副作用前保存：调用工具的意图在工具
	// 执行前已耐久。
	BarrierToolCall
)

// WithInterval sets how often checkpoints are saved (every N steps).
// Default: 5.
//
// WithInterval 设置检查点保存频率（每 N 步保存一次）。默认 5。
func WithInterval(n int) AutoSaveOption {
	return func(c *autoSaveConfig) {
		if n > 0 {
			c.interval = n
		}
	}
}

// WithSessionID tags every checkpoint with the caller's session identifier.
// Consumers that run many sessions against one shared store (e.g. a web
// service) need the tag to locate the latest checkpoint per session.
//
// WithSessionID 为每个检查点打上调用方会话标识。多个会话共用同一存储的
// 消费方（如 Web 服务）依赖该标识按会话定位最新检查点。
func WithSessionID(id string) AutoSaveOption {
	return func(c *autoSaveConfig) {
		if id != "" {
			c.sessionID = id
		}
	}
}

// WithBarriers enables the semantic persistence barriers at the selected
// boundaries (OR-combined: BarrierModelCall|BarrierToolCall). Each barrier
// saves the checkpoint before the boundary and fails closed on save
// failure — an undurable execution intent never proceeds.
//
// WithBarriers 启用所选边界的语义持久化屏障（按位或组合：
// BarrierModelCall|BarrierToolCall）。每个屏障在边界前保存检查点，
// 保存失败即 fail-closed——不可久化的执行意图绝不推进。
func WithBarriers(b Barrier) AutoSaveOption {
	return func(c *autoSaveConfig) { c.barriers = b }
}

// AutoSaveModule persists periodic checkpoints via the OnStepEnd hook.
// It replaces the checkpoint logic that used to live inside the StepLoop
// engine: the loop core stays a pure loop, and persistence is a cross-cutting
// concern expressed as a module, like module/session.
//
// Semantics: a checkpoint is saved on every interval-th step (step > 0),
// reusing one checkpoint ID so the store always holds the most recent
// recovery point. The policy session snapshot (StepInfo.PolicyState) is
// persisted as the checkpoint's State, so Runner.Resume continues from the
// saved step instead of replaying messages. Pair it with a CheckpointStore
// on the Runner (runner.WithRunnerCheckpointStore).
//
// With WithBarriers, the module additionally acts as a semantic persistence
// barrier (DSH session-checkpoint-policy): before each selected boundary
// the checkpoint is saved, and a save failure blocks the boundary —
// fail-closed. The step-interval autosave stays best-effort (logged), the
// barriers are the durability contract.
//
// AutoSaveModule 通过 OnStepEnd 钩子周期保存检查点，取代原先内建于
// StepLoop 引擎的保存逻辑：循环内核保持纯净，持久化作为横切关注点
// 以模块表达（与 module/session 一致）。
// 语义：每第 N 步（step > 0）保存一次，复用同一检查点 ID，存储中始终
// 保留最新恢复点。策略会话快照（StepInfo.PolicyState）作为检查点 State
// 持久化，使 Runner.Resume 从保存的步骤继续而非重放消息。
// 配合 Runner 上的 CheckpointStore 使用（runner.WithRunnerCheckpointStore）。
// 启用 WithBarriers 后，模块同时充当语义持久化屏障（DSH
// session-checkpoint-policy）：每个所选边界前保存检查点，保存失败阻断
// 边界——fail-closed。步间隔自动保存保持尽力而为（记日志），屏障是
// 耐久契约。
type AutoSaveModule struct {
	mu        sync.Mutex
	store     kernel.CheckpointStore
	interval  int
	sessionID string
	lastID    string
	barriers  Barrier
}

// NewAutoSaveModule creates an auto-save module for the given store.
//
// NewAutoSaveModule 为给定存储创建自动保存模块。
func NewAutoSaveModule(store kernel.CheckpointStore, opts ...AutoSaveOption) *AutoSaveModule {
	cfg := &autoSaveConfig{interval: 5}
	for _, o := range opts {
		o(cfg)
	}
	return &AutoSaveModule{
		store: store, interval: cfg.interval, sessionID: cfg.sessionID,
		barriers: cfg.barriers,
	}
}

// Register registers the hooks on the runtime: the step-interval autosave,
// plus the semantic barriers when enabled.
//
// Register 在运行时上注册钩子：步间隔自动保存，以及启用时的语义屏障。
func (m *AutoSaveModule) Register(rt kernel.HookRegistrar) {
	rt.OnStepEnd(m.onStepEnd)
	if m.barriers&BarrierModelCall != 0 {
		rt.OnModelCall(m.onModelCall)
	}
	if m.barriers&BarrierToolCall != 0 {
		rt.OnToolCall(m.onToolCall)
	}
}

// onModelCall is the model-request barrier: the messages that produced the
// request are durable before the request is sent. A save failure blocks the
// call (fail-closed — the request is not worth sending if the intent that
// produced it cannot be recovered). The barrier bit gates the method so a
// direct call without WithBarriers stays a pass-through.
func (m *AutoSaveModule) onModelCall(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	if m.barriers&BarrierModelCall == 0 || m.store == nil || info == nil {
		return ctx, info, nil
	}
	if err := m.saveSnapshot(ctx, info.AgentName, len(info.Messages), 0, info.Messages); err != nil {
		return ctx, nil, fmt.Errorf("checkpoint barrier: model call blocked: %w", err)
	}
	return ctx, info, nil
}

// onToolCall is the tool side-effect barrier: the intent to call the tool
// is durable before the tool runs. A save failure blocks the call
// (fail-closed — an unrecoverable intent must not execute a side effect).
// The barrier bit gates the method so a direct call without WithBarriers
// stays a pass-through.
func (m *AutoSaveModule) onToolCall(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	if m.barriers&BarrierToolCall == 0 || m.store == nil || info == nil {
		return ctx, info, nil
	}
	if err := m.saveSnapshot(ctx, info.AgentName, info.StepIndex, 0, nil); err != nil {
		return ctx, nil, fmt.Errorf("checkpoint barrier: tool call blocked: %w", err)
	}
	return ctx, info, nil
}

// onStepEnd saves a checkpoint every interval-th step, reusing one ID so
// the store always keeps the most recent recovery point. The policy session
// snapshot (if any) is persisted as the checkpoint's State, enabling resume
// from the saved step. Save failures are non-fatal (logged).
func (m *AutoSaveModule) onStepEnd(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
	if m.store == nil || info == nil || info.StepIndex <= 0 || info.StepIndex%m.interval != 0 {
		return ctx, info, nil
	}
	if err := m.saveSnapshot(ctx, info.AgentName, info.StepIndex, info.MaxSteps, info.Messages); err != nil {
		log.Printf("[checkpoint] autosave: %v", err) // non-fatal
	}
	// The policy snapshot rides on the same checkpoint: load the just-saved
	// record and attach the serialized state. Failures are non-fatal but
	// never silent — a lost policy state (plan mode / HITL) degrades resume
	// to a fresh session, and that must be observable.
	if info.PolicyState != nil {
		m.mu.Lock()
		b, err := json.Marshal(info.PolicyState)
		if err != nil {
			m.mu.Unlock()
			log.Printf("[checkpoint] policy state marshal: %v", err)
			return ctx, info, nil
		}
		last, err := m.store.Load(ctx, m.lastID)
		if err != nil {
			m.mu.Unlock()
			log.Printf("[checkpoint] policy state load: %v", err)
			return ctx, info, nil
		}
		last.State = b
		if err := m.store.Save(ctx, last); err != nil {
			log.Printf("[checkpoint] policy state save: %v", err)
		}
		m.mu.Unlock()
	}
	return ctx, info, nil
}

// saveSnapshot saves a checkpoint with the given identity and message
// snapshot, reusing one ID per module. Barrier callers treat an error as a
// fail-closed block; the step autosave logs it.
func (m *AutoSaveModule) saveSnapshot(ctx context.Context, agentName string, step, maxSteps int, msgs []*types.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil {
		return nil
	}
	if m.lastID == "" {
		m.lastID = types.CheckpointID()
	}
	cp := &types.Checkpoint{
		ID:        m.lastID,
		AgentName: agentName,
		SessionID: m.sessionID,
		Messages:  types.CloneMessages(msgs),
		MaxSteps:  maxSteps,
		StepIndex: step,
		CreatedAt: time.Now(),
	}
	return m.store.Save(ctx, cp)
}
