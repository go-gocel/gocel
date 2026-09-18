// Package engine provides the StepLoop: the generic ReAct agent loop.
//
// The loop is the MECHANISM that decides HOW an agent runs:
// model call → tool execution → observe → repeat. The strategy is the
// minimal ReAct rule: keep looping while the model emits tool calls, and
// finish with the last assistant message once it answers without tools.
//
// Package engine 提供 StepLoop：通用的 ReAct 循环。循环是机制，决定 agent
// 怎么跑（模型调用 → 工具执行 → 观察 → 重复）。策略固定为最小 ReAct 规则：
// 模型发出工具调用就继续循环，不再调用工具时以最后一条 assistant 消息收尾。
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"

	coreengine "github.com/go-gocel/gocel/core/engine"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

const defaultMaxSteps = 25

// StepLoop is the generic ReAct agent loop. It drives the model/tool cycle
// until the model answers without tool calls or the step budget is exhausted.
//
// StepLoop 是通用 ReAct 循环。它驱动模型/工具循环，直到模型不再调用工具
// 直接作答，或步数预算耗尽。
type StepLoop struct {
	maxSteps int
}

// Option configures a StepLoop.
// Option 配置 StepLoop。
type Option func(*StepLoop)

// WithMaxSteps sets the step budget used when RunInput.MaxSteps <= 0.
// WithMaxSteps 设置 RunInput.MaxSteps <= 0 时使用的步数预算。
func WithMaxSteps(n int) Option {
	return func(s *StepLoop) {
		if n > 0 {
			s.maxSteps = n
		}
	}
}

// New builds a StepLoop.
// New 构建一个 StepLoop。
func New(opts ...Option) *StepLoop {
	s := &StepLoop{maxSteps: defaultMaxSteps}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ coreengine.Engine = (*StepLoop)(nil)

// Run executes the ReAct loop and returns the result.
// Run 执行 ReAct 循环并返回结果。
func (s *StepLoop) Run(ctx context.Context, input *coreengine.RunInput, rt kernel.Runtime) *kernel.Result {
	if input == nil {
		return &kernel.Result{Reason: kernel.TerminateError, Err: errors.New("engine: nil input")}
	}
	msgs := input.Messages
	if len(msgs) == 0 {
		return &kernel.Result{Reason: kernel.TerminateError, Err: errors.New("engine: no input messages")}
	}
	maxSteps := input.MaxSteps
	if maxSteps <= 0 {
		maxSteps = s.maxSteps
	}
	send := input.SendEvent
	if send == nil {
		if ac := kernel.GetAgentContext(ctx); ac != nil {
			send = ac.SendEvent()
		}
	}

	var totalUsage *types.TokenUsage
	for step := 0; step < maxSteps; step++ {
		if ctx.Err() != nil {
			return &kernel.Result{Messages: msgs, TokenUsage: totalUsage, Reason: kernel.TerminateCanceled, Err: ctx.Err()}
		}

		stepInfo := &kernel.StepInfo{
			AgentName: input.AgentName,
			Messages:  msgs,
			StepIndex: step,
			MaxSteps:  maxSteps,
		}

		var (
			cont    bool
			err     error
			stepRes *kernel.Result
		)

		ctx, cont, stepInfo, err = runStep(rt, ctx, stepInfo, func(ctx context.Context, info *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
			stepMsgs := info.Messages

			resp, usage, modelErr := callModel(ctx, rt, stepMsgs, input.InitialGenOpts, input.StreamMode, send)
			if modelErr != nil {
				return ctx, info, fmt.Errorf("step %d: %w", step, modelErr)
			}
			if resp == nil {
				return ctx, info, fmt.Errorf("step %d: empty model response", step)
			}
			if usage != nil {
				totalUsage = accumulateUsage(totalUsage, usage)
			}

			stepMsgs = append(stepMsgs, resp)
			hasToolCalls := len(resp.ToolCalls) > 0
			if hasToolCalls {
				stepMsgs = append(stepMsgs, execToolPhase(ctx, rt, resp, send)...)
			}

			info.Messages = stepMsgs
			info.HasToolCalls = hasToolCalls

			if !hasToolCalls {
				stepRes = finishedResult(stepMsgs, totalUsage)
			}
			return ctx, info, nil
		})

		if err != nil {
			return &kernel.Result{Messages: msgs, TokenUsage: totalUsage, Reason: kernel.TerminateError, Err: err}
		}
		if stepRes != nil {
			return stepRes
		}
		if !cont {
			return finishedResult(msgs, totalUsage)
		}
		if stepInfo != nil && stepInfo.Messages != nil {
			msgs = stepInfo.Messages
		}
	}

	return &kernel.Result{
		Content:    lastContent(msgs),
		Messages:   msgs,
		TokenUsage: totalUsage,
		Reason:     kernel.TerminateMaxSteps,
	}
}

// stepLifecycler is the optional capability a Runtime exposes to run one step
// with its hook lifecycle centralized (the concrete runtime implements it as
// RunStep). Runtimes without it fall back to FireStepStart/FireStepEnd.
type stepLifecycler interface {
	RunStep(ctx context.Context, info *kernel.StepInfo, body func(context.Context, *kernel.StepInfo) (context.Context, *kernel.StepInfo, error)) (context.Context, bool, *kernel.StepInfo, error)
}

// runStep runs one step body inside the StepStart/StepEnd lifecycle. It
// prefers the Runtime's centralized RunStep; otherwise it fires the hooks
// directly, still guaranteeing StepEnd covers every exit path.
func runStep(rt kernel.Runtime, ctx context.Context, info *kernel.StepInfo, body func(context.Context, *kernel.StepInfo) (context.Context, *kernel.StepInfo, error)) (context.Context, bool, *kernel.StepInfo, error) {
	if sl, ok := rt.(stepLifecycler); ok {
		return sl.RunStep(ctx, info, body)
	}

	var (
		cont bool
		err  error
	)
	ctx, cont, info, err = rt.FireStepStart(ctx, info)
	if err != nil || !cont {
		return ctx, cont, info, err
	}
	defer func() {
		endCtx, endCont, endInfo, endErr := rt.FireStepEnd(ctx, info)
		_ = endCtx
		cont = endCont
		if endInfo != nil {
			info = endInfo
		}
		if err == nil {
			err = endErr
		} else if endErr != nil {
			err = errors.Join(err, endErr)
		}
	}()
	ctx, info, err = body(ctx, info)
	return ctx, cont, info, err
}

// callModel performs one model call, streaming per-token events when both
// stream mode and an event sender are available.
func callModel(ctx context.Context, rt kernel.Runtime, msgs []*types.Message, genOpts []kernel.GenOption, stream bool, send func(*types.Event) bool) (*types.Message, *types.TokenUsage, error) {
	if !stream || send == nil {
		return rt.CallModel(ctx, msgs, genOpts...)
	}
	reader, err := rt.CallModelStream(ctx, msgs, genOpts...)
	if err != nil {
		return nil, nil, err
	}
	resp, err := collectStream(reader, send)
	if err != nil {
		return nil, nil, err
	}
	return resp, nil, nil
}

// collectStream drains a streaming response, forwarding token/reasoning
// events and merging tool-call deltas by index.
func collectStream(reader kernel.StreamReader, send func(*types.Event) bool) (*types.Message, error) {
	defer reader.Close()
	resp := &types.Message{Role: types.RoleAssistant}
	var (
		content   string
		reasoning string
		index     map[int]int
	)
	for {
		chunk, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if chunk == nil {
			continue
		}
		if chunk.Content != "" {
			content += chunk.Content
			send(types.TokenEvent(chunk.Content))
		}
		if chunk.ReasoningContent != "" {
			reasoning += chunk.ReasoningContent
			send(types.ReasoningEvent(chunk.ReasoningContent))
		}
		for _, tc := range chunk.ToolCalls {
			mergeStreamToolCall(resp, tc, &index)
		}
	}
	resp.Content = content
	resp.ReasoningContent = reasoning
	return resp, nil
}

// mergeStreamToolCall merges one tool-call delta into resp, keyed by the
// delta's Index (nil deltas append whole).
func mergeStreamToolCall(resp *types.Message, tc types.ToolCall, index *map[int]int) {
	if tc.Index == nil {
		resp.ToolCalls = append(resp.ToolCalls, tc)
		return
	}
	if *index == nil {
		*index = map[int]int{}
	}
	pos, ok := (*index)[*tc.Index]
	if !ok {
		pos = len(resp.ToolCalls)
		(*index)[*tc.Index] = pos
		resp.ToolCalls = append(resp.ToolCalls, types.ToolCall{Index: tc.Index})
	}
	t := &resp.ToolCalls[pos]
	if t.ID == "" {
		t.ID = tc.ID
	}
	if t.Type == "" {
		t.Type = tc.Type
	}
	t.Function.Name += tc.Function.Name
	t.Function.Arguments += tc.Function.Arguments
}

// execToolPhase executes the model's tool calls and returns the tool
// messages, emitting tool-call/tool-result events when a sender is present.
func execToolPhase(ctx context.Context, rt kernel.Runtime, resp *types.Message, send func(*types.Event) bool) []*types.Message {
	calls := make([]*types.ToolCall, len(resp.ToolCalls))
	for i := range resp.ToolCalls {
		calls[i] = &resp.ToolCalls[i]
	}
	if send != nil {
		for _, tc := range calls {
			send(types.ToolCallEvent(tc.Function.Name, tc.Function.Arguments, tc.ID))
		}
	}
	results := rt.ExecTools(ctx, calls)
	if send != nil {
		for _, m := range results {
			send(types.ToolResultEvent(m.ToolName, m.Content, m.ToolCallID))
		}
	}
	return results
}

// finishedResult builds a finished result whose content is the last assistant
// message.
func finishedResult(msgs []*types.Message, usage *types.TokenUsage) *kernel.Result {
	return &kernel.Result{Content: lastContent(msgs), Messages: msgs, TokenUsage: usage, Reason: kernel.TerminateFinished}
}

// lastContent returns the last assistant message's text content.
func lastContent(msgs []*types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m != nil && m.Role == types.RoleAssistant && m.Content != "" {
			return m.Content
		}
	}
	return ""
}

// accumulateUsage adds one call's usage into the running total.
func accumulateUsage(total, add *types.TokenUsage) *types.TokenUsage {
	if add == nil {
		return total
	}
	if total == nil {
		return &types.TokenUsage{
			PromptTokens:     add.PromptTokens,
			CompletionTokens: add.CompletionTokens,
			TotalTokens:      add.TotalTokens,
		}
	}
	total.PromptTokens += add.PromptTokens
	total.CompletionTokens += add.CompletionTokens
	total.TotalTokens += add.TotalTokens
	return total
}
