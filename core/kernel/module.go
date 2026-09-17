package kernel

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// HookRegistrar is the interface through which Modules register lifecycle
// hooks. It exposes only registration methods — Modules see nothing about
// model calls or tool execution internals.
//
// Hook points follow the Harness lifecycle layers:
//
//	run 级      OnAgentStart / OnAgentEnd
//	上下文工程  OnMessagesBuilt
//	循环级      OnStepStart / OnStepEnd
//	模型调用级  OnModelCall / OnModelResult
//	工具执行级  OnToolCall / OnToolResult
//
// Modules sit ABOVE the runtime: they observe and intervene in the whole
// lifecycle. They must not wrap model calls — that is the Middleware layer,
// which lives INSIDE the runtime, directly above ChatModel.
//
// HookRegistrar 是 Module 注册生命周期钩子的接口，只暴露注册方法。
// 钩子点按 Harness 生命周期分层（见上）。模块位于 runtime 之上，
// 观察/干预整个生命周期；模型调用的包装属于中间件层（runtime 内部）。
type HookRegistrar interface {
	OnAgentStart(fn AgentStartHook) func()
	OnAgentEnd(fn AgentEndHook) func()
	OnMessagesBuilt(fn MessagesHook) func()
	OnStepStart(fn StepHook) func()
	OnStepEnd(fn StepHook) func()
	OnModelCall(fn ModelCallHook) func()
	OnModelResult(fn ModelResultHookFunc) func()
	OnToolCall(fn ToolCallHook) func()
	OnToolResult(fn ToolResultHookFunc) func()
	OnDecision(fn DecisionHook) func()
}

// ❄️ FROZEN — Stable type signatures. Must not change.
//
// ModelResultHookFunc is an alias of ModelCallHook kept for readability of
// the after-call registration point.
//
// ModelResultHookFunc 是 ModelCallHook 的别名，用于让调用后注册点更易读。
type ModelResultHookFunc = ModelCallHook

// ❄️ FROZEN — Stable type signatures. Must not change.
//
// ToolResultHookFunc is an alias of ToolCallHook kept for readability of the
// after-call registration point.
//
// ToolResultHookFunc 是 ToolCallHook 的别名，用于让调用后注册点更易读。
type ToolResultHookFunc = ToolCallHook

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// Module defines a cross-cutting concern of the Harness: a named bundle of
// hooks (plus optional resources injected at construction) that observes and
// intervenes in the execution lifecycle. Modules are assembled into the
// Runtime by the harness builder — they are the primary extension mechanism
// for engineering concerns (memory, safety, session, observability, HITL).
//
// Module 定义 Harness 的横切关注点：一组钩子（加上构造时注入的可选资源），
// 观察并干预执行生命周期。模块由 harness 装配器组装进 Runtime——
// 是工程化关注点（记忆、安全、会话、可观测性、HITL）的主要扩展机制。
type Module interface {
	Register(rt HookRegistrar)
}
