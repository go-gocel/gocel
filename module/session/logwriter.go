// Package session 的日志写入模块：LogWriter 把引擎生命周期钩子翻译为
// 会话日志事件——step/turn/tool 词汇的生产方。它解决"事件有消费方
// （sessionstats 等投影）却无生产方"的集成缺口：StepLoop 引擎只发运行时
// types.Event，不写会话日志；LogWriter 作为模块观察 OnAgentStart/OnStepStart/
// OnStepEnd/OnToolCall 钩子，把生命周期翻译成 core/session 的持久事件，
// 使投影单元（sessionstats）与实际运行数据源接通。
//
// 事件词汇（独立命名空间，避免与预定义 kind 的载荷约定冲突）：
//
//   - "turn/start"（OnAgentStart：一次 Run = 一个轮次）
//   - "step/start"（OnStepStart）
//   - "step/end"  （OnStepEnd）
//   - "session/model_usage"（OnModelResult，携带本次模型调用的 token 用量
//     增量：prompt_tokens / completion_tokens / total_tokens —— sessionstats
//     等投影消费它累加；与 logSession 的 "session/usage" 累计词汇并列，
//     载荷形状不同，绝不复用同一 kind）
//   - "session/tool_call"（OnToolCall）
//   - "session/tool_result"（OnToolResult，携带结果）
//
// LogWriter 是可选的：不挂载时引擎照常运行，日志只是没有生命周期事件。
package session

import (
	"context"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// LogWriter writes lifecycle events into a session log. It is the
// producer side of the session-stats event vocabulary.
//
// LogWriter 把生命周期事件写入会话日志。它是 session-stats 事件词汇的
// 生产方。
type LogWriter struct {
	log *coresession.Log
}

// NewLogWriter creates a writer over the given log.
// NewLogWriter 在给定日志上创建写入器。
func NewLogWriter(log *coresession.Log) *LogWriter {
	return &LogWriter{log: log}
}

// Register implements kernel.Module.
//
// Register 实现 kernel.Module：把引擎生命周期钩子（agent/step/model/tool）
// 注册为会话日志事件的生产源。
func (w *LogWriter) Register(rt kernel.HookRegistrar) {
	if w == nil || w.log == nil {
		return
	}
	rt.OnAgentStart(w.onAgentStart)
	rt.OnStepStart(w.onStepStart)
	rt.OnStepEnd(w.onStepEnd)
	rt.OnModelResult(w.onModelResult)
	rt.OnToolCall(w.onToolCall)
	rt.OnToolResult(w.onToolResult)
}

// onAgentStart records the turn boundary: one Run is one turn.
func (w *LogWriter) onAgentStart(ctx context.Context, info *kernel.AgentRunInfo) (context.Context, *kernel.AgentRunInfo, error) {
	if info == nil {
		return ctx, info, nil
	}
	_, _ = w.log.Append(types.NewLogOnlyEvent("turn/start", map[string]any{"agent": info.AgentName}))
	return ctx, info, nil
}

// onStepStart records the step boundary.
func (w *LogWriter) onStepStart(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
	if info == nil {
		return ctx, info, nil
	}
	_, _ = w.log.Append(types.NewLogOnlyEvent("step/start", map[string]any{"step": info.StepIndex, "max_steps": info.MaxSteps}))
	return ctx, info, nil
}

// onStepEnd records the step end.
func (w *LogWriter) onStepEnd(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
	if info == nil {
		return ctx, info, nil
	}
	meta := map[string]any{"step": info.StepIndex, "has_tool_calls": info.HasToolCalls}
	_, _ = w.log.Append(types.NewLogOnlyEvent("step/end", meta))
	return ctx, info, nil
}

// onModelResult records the model call's token usage as a
// "session/model_usage" delta event — the producer side of the token
// vocabulary for stats projections. The usage is written as deltas (each
// call's numbers), so replaying the log reconstructs the exact totals.
// Calls without usage contribute nothing.
func (w *LogWriter) onModelResult(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	if info == nil || info.Usage == nil {
		return ctx, info, nil
	}
	_, _ = w.log.Append(types.NewLogOnlyEvent("session/model_usage", map[string]any{
		"prompt_tokens":     info.Usage.PromptTokens,
		"completion_tokens": info.Usage.CompletionTokens,
		"total_tokens":      info.Usage.TotalTokens,
	}))
	return ctx, info, nil
}

// onToolCall records the tool invocation (the result event follows on
// completion).
func (w *LogWriter) onToolCall(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	if info == nil {
		return ctx, info, nil
	}
	_, _ = w.log.Append(types.NewLogOnlyEvent("session/tool_call", map[string]any{"tool": info.Name, "step": info.StepIndex}))
	return ctx, info, nil
}

// onToolResult records the tool completion.
func (w *LogWriter) onToolResult(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	if info == nil {
		return ctx, info, nil
	}
	meta := map[string]any{"tool": info.Name, "step": info.StepIndex}
	if info.Result != "" {
		meta["result"] = info.Result
	}
	_, _ = w.log.Append(types.NewLogOnlyEvent("session/tool_result", meta))
	return ctx, info, nil
}
