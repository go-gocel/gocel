package kernel

import (
	"context"
	"errors"

	"github.com/go-gocel/gocel/core/types"
)

// ── Hook 分层 ─────────────────────────────────────────────────────────────
//
// 钩子是 Harness 的横切扩展点：Module 通过 HookRegistrar 注册钩子，
// 观察/干预执行生命周期。钩子按层级分组：
//
//	run 级      OnAgentStart / OnAgentEnd       —— 一次执行的开/结尾
//	上下文工程  OnMessagesBuilt                 —— 输入消息构建完成
//	循环级      OnStepStart / OnStepEnd         —— 每步循环的前/后
//	模型调用级  OnModelCall / OnModelResult     —— 模型调用前后（Runtime 内部触发）
//	工具执行级  OnToolCall / OnToolResult       —— 工具执行前后（Runtime 内部触发）
//
// 层级边界：钩子可以看到循环、消息、工具（修改/中断/注入），位于
// Runtime 的模型/工具调用之外；链式中间件（Middleware）只能看到模型
// 调用（msgs → resp），位于 Runtime 内部、ChatModel 之上。
// 两者不重叠：钩子面向 Harness 生命周期，中间件面向模型调用链。

// ❄️ FROZEN — Stable type signatures. Must not change.
//
// AgentStartHook fires once at the beginning of an agent run, before the
// agent executes. Modules can inspect or modify run identity/input.
//
// AgentStartHook 在 Agent 执行开始前触发一次。模块可检查或修改运行身份/输入。
type AgentStartHook func(ctx context.Context, info *AgentRunInfo) (context.Context, *AgentRunInfo, error)

// ❄️ FROZEN — Stable type signatures. Must not change.
//
// AgentEndHook fires once after an agent run completes. Modules can inspect
// or modify the run info (e.g. persist the session).
//
// AgentEndHook 在 Agent 执行完成后触发一次。模块可检查或修改运行信息
// （如持久化会话）。
type AgentEndHook func(ctx context.Context, info *RunInfo) (context.Context, *RunInfo, error)

// ❄️ FROZEN — Stable type signatures. Must not change.
//
// MessagesHook fires after the full message list (system + user + strategy
// instructions) has been built, once per agent run. Modules can inspect or
// modify the message list (e.g. inject memory into the system prompt).
//
// Contract: modules may append additional messages (system / user / ...).
// FireMessagesBuilt collapses appended system messages into the single
// leading system message (single-system invariant). Appended user/assistant/
// tool messages are kept as-is.
//
// MessagesHook 在完整消息列表（系统 + 用户 + 策略指令）构建完成后触发一次。
// 模块可检查或修改消息列表（如向系统提示注入记忆）。
//
// 契约：模块可追加消息（system / user / ...）。FireMessagesBuilt 会把追加的
// system 消息合并进唯一一条前置 system（单 system 不变量）；追加的
// user / assistant / tool 消息原样保留。
type MessagesHook func(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error)

// ❄️ FROZEN — Stable type signatures. Must not change.
//
// StepHook fires before/after each loop step. Modules can modify StepInfo
// (messages, continue flag) or abort the loop.
//
// StepHook 在每步循环前后触发。模块可修改 StepInfo（消息、继续标记）或中止循环。
type StepHook func(ctx context.Context, info *StepInfo) (context.Context, *StepInfo, error)

// ❄️ FROZEN — Stable type signatures. Must not change.
//
// ModelCallHook fires before a model generation call (OnModelCall) and after
// it completes (OnModelResult). Modules can inspect or modify the call.
//
// ModelCallHook 在模型调用前（OnModelCall）和完成后（OnModelResult）触发。
// 模块可检查或修改调用。
type ModelCallHook func(ctx context.Context, info *ModelCallInfo) (context.Context, *ModelCallInfo, error)

// ❄️ FROZEN — Stable type signatures. Must not change.
//
// ToolCallHook fires before a tool is executed (OnToolCall) and after it
// completes (OnToolResult). Modules can inspect, modify, or block the call.
//
// ToolCallHook 在工具执行前（OnToolCall）和完成后（OnToolResult）触发。
// 模块可检查、修改或阻断调用。
type ToolCallHook func(ctx context.Context, info *ToolCallInfo) (context.Context, *ToolCallInfo, error)

// ── Hook 数据类型 ──────────────────────────────────────────────────────

// AgentRunInfo carries context for run-level hooks.
// AgentRunInfo 携带 run 级钩子的上下文信息。
type AgentRunInfo struct {
	AgentName    string
	InvocationID string
	RunPath      string
	Branch       string
	Input        *types.AgentInput
}

// ModelCallInfo carries context for model call hooks.
//
// InvocationID/AgentName/StepIndex 由 Runtime 在触发钩子时统一填充，
// 使审计与观测能直接按「运行 → 步骤 → 模型调用」串起调用链，
// 无需各模块自行从 context 提取。StepIndex 为 -1 表示不在 step 循环内。
//
// ModelCallInfo 携带模型调用钩子的上下文信息。
type ModelCallInfo struct {
	InvocationID string
	AgentName    string
	StepIndex    int
	Messages     []*types.Message
	Response     *types.Message
	Usage        *types.TokenUsage
	Error        error
}

// ToolCallInfo carries context for tool call hooks.
//
// InvocationID/AgentName/StepIndex 由 Runtime 在触发钩子时统一填充（见
// ModelCallInfo）。Effects 由 Runtime 从工具的声明（或保守默认）统一
// 填充——守卫/审批模块直接消费，不再各自解析工具。
//
// ToolCallInfo 携带工具调用钩子的上下文信息。
type ToolCallInfo struct {
	InvocationID string
	AgentName    string
	StepIndex    int
	Name         string
	Args         string
	// Effects is the tool's declared side-effect set (conservative default
	// when undeclared), filled by the Runtime at hook time.
	//
	// Effects 是工具声明的副作用集合（未声明时按保守默认），
	// 由 Runtime 在钩子触发时统一填充。
	Effects []ToolEffect
	Result  string
	// Parts carries multimodal content parts attached to the tool result.
	// On OnToolResult the Runtime fills it from the executed message. A hook
	// may set it to attach images/files in the same step. Write-back honors:
	// nil = leave the tool's parts unchanged; a non-nil empty slice =
	// explicitly clear them.
	//
	// Parts 携带工具结果的多模态内容片段。OnToolResult 触发时由 Runtime
	// 从执行得到的消息填充。钩子可设置它以附加图片/文件。写回语义：
	// nil = 保留工具原有 parts 不变；非 nil 空切片 = 显式清空。
	Parts []types.ContentPart
	Error error
}

// StepInfo carries context for step lifecycle hooks.
//
// InvocationID 由 Runtime 在 FireStepStart/FireStepEnd 时统一填充。
//
// StepInfo 携带步骤生命周期钩子的上下文信息。
type StepInfo struct {
	InvocationID string
	AgentName    string
	Messages     []*types.Message
	StepIndex    int
	// MaxSteps is the step budget of this run; checkpointing uses it to
	// compute the remaining steps on resume.
	//
	// MaxSteps 是本次运行的步数预算；检查点用它计算恢复后的剩余步数。
	MaxSteps     int
	HasToolCalls bool
	Continue     bool
}

// ── RetryPolicy ──────────────────────────────────────────────────────────
// Moved to core/runtime. Import "github.com/go-gocel/gocel/core/runtime" for RetryPolicy.

// DecisionInfo describes a guard/approval decision made during tool
// interception: who decided, on what, with which outcome.
//
// DecisionInfo 描述一次守卫/审批决策（批准、拒绝、硬阻断等），
// 供审计与观测模块统一消费。
type DecisionInfo struct {
	InvocationID string
	AgentName    string
	StepIndex    int
	// Tool is the intercepted tool name.
	Tool string
	// Args is the intercepted input (command/path/raw arguments).
	Args string
	// Decision is the outcome: approved / rejected / hard_blocked /
	// policy_deny / timeout_rejected / timeout_skip / timeout_approve。
	Decision string
	// Reason explains the decision.
	Reason string
}

// DecisionHook fires when a guard/approval module makes a decision about a
// tool call. Modules may observe or amend the decision.
//
// DecisionHook 在守卫/审批模块对工具调用作出决策时触发。
// 模块可观察或修正决策。
type DecisionHook func(ctx context.Context, info *DecisionInfo) (context.Context, *DecisionInfo, error)

// IsRetryableError checks whether an error should trigger a retry.
// Non-retryable errors include InputError, NotFoundError, and CircuitError.
//
// IsRetryableError 判断错误是否应触发重试。
// InputError、NotFoundError、CircuitError 不可重试。
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// The caller decided to stop: cancellation is never a retry trigger,
	// even when a provider wraps it (C-series: the default-true fallback
	// used to classify context.Canceled/DeadlineExceeded as retryable).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if IsRetryable(err) {
		return true
	}
	// A model error that did not classify retryable is permanent BY
	// DECLARATION — it must not fall through to the default.
	var me *ModelError
	if errors.As(err, &me) {
		return false
	}
	if IsRateLimitError(err) {
		return true
	}
	if IsCircuitError(err) {
		return false
	}
	if IsInputError(err) || IsNotFound(err) {
		return false
	}
	return true
}
