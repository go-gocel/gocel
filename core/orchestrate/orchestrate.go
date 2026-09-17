package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ─── Parallel ────────────────────────────────────────────────────────────

// ParallelOption configures parallel execution behavior.
//
// ParallelOption 配置并行执行行为。
type ParallelOption func(*parallelConfig)

type parallelConfig struct {
	maxConcurrency int
	timeout        time.Duration // 批次整体超时（0 = 不限）
	mergeFunc      func(name string, results []*kernel.Result) *kernel.Result
}

// WithMaxConcurrency limits the number of concurrently executing agents.
// Default: all agents run concurrently.
//
// WithMaxConcurrency 限制最大并发 Agent 数。默认全部并发执行。
// WithMaxConcurrency limits the number of concurrently executing agents.
// Default: all agents run concurrently.
//
// WithMaxConcurrency 限制最大并发 Agent 数。默认全部并发执行。
func WithMaxConcurrency(n int) ParallelOption {
	return func(c *parallelConfig) {
		c.maxConcurrency = n
	}
}

// WithTimeout sets a wall-clock timeout for the whole parallel batch. When
// the timeout expires, the batch context is cancelled and in-flight agents
// observe ctx cancellation. Zero disables the timeout.
//
// WithTimeout 设置并行批次的整体超时。超时后批次上下文被取消，
// 运行中的 Agent 会观察到 ctx 取消。零值表示不限制。
func WithTimeout(d time.Duration) ParallelOption {
	return func(c *parallelConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// Parallel executes multiple Agents concurrently and merges results.
// Each Agent receives its own copy of the input.
// Uses errgroup + semaphore for concurrency control.
// Returns an Agent that blocks RunNew until all sub-agents complete.
//
// Parallel 并发执行多个 Agent 并合并结果。每个 Agent 接收输入的副本。
// 使用 errgroup + semaphore 控制并发。返回一个阻塞直到全部完成的 Agent。
func Parallel(agents []kernel.Agent, opts ...ParallelOption) kernel.Agent {
	cfg := &parallelConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	// Collect names for the composite agent
	var names string
	for i, a := range agents {
		if i > 0 {
			names += "+"
		}
		names += a.Name()
	}

	return &parallelAgent{
		agents: agents,
		config: cfg,
		name:   "parallel:" + names,
	}
}

type parallelAgent struct {
	agents []kernel.Agent
	config *parallelConfig
	name   string
}

// Name returns the composite agent's identifier.
// Name 返回组合 Agent 的标识。
func (p *parallelAgent) Name() string        { return p.name }
// Description returns a description of the parallel agent.
// Description 返回并行 Agent 的描述。
func (p *parallelAgent) Description() string { return "Parallel execution of " + p.name }

// Run executes all sub-agents concurrently and returns the merged result.
// Run 并发执行所有子 Agent 并返回合并结果。
func (p *parallelAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	if len(p.agents) == 0 {
		return &kernel.Result{}
	}

	// Apply the batch timeout: the derived context cancels in-flight agents.
	if p.config.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.config.timeout)
		defer cancel()
	}

	// Limit concurrency if configured
	maxConc := len(p.agents)
	if p.config.maxConcurrency > 0 && p.config.maxConcurrency < maxConc {
		maxConc = p.config.maxConcurrency
	}

	type agentResult struct {
		index  int
		result *kernel.Result
	}

	resultCh := make(chan agentResult, len(p.agents))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	results := make([]*kernel.Result, len(p.agents))

	for i, a := range p.agents {
		wg.Add(1)
		sem <- struct{}{}
		i := i
		a := a
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				// A panicking agent must not crash the host: surface it as
				// an error result (C3).
				if r := recover(); r != nil {
					results[i] = &kernel.Result{
						Err:    fmt.Errorf("parallel agent %q panicked: %v", a.Name(), r),
						Reason: kernel.TerminateError,
					}
				}
			}()
			res := a.Run(ctx, input, rt)
			resultCh <- agentResult{index: i, result: res}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for ar := range resultCh {
		results[ar.index] = ar.result
	}

	return mergeResults(p.agents, results)
}

func mergeResults(agents []kernel.Agent, results []*kernel.Result) *kernel.Result {
	var combinedMessages []*types.Message
	var totalUsage *types.TokenUsage
	var content string
	var firstErr error

	for i, res := range results {
		if res == nil {
			continue
		}
		if res.Err != nil && firstErr == nil {
			firstErr = res.Err
		}
		if len(res.Messages) > 0 {
			combinedMessages = append(combinedMessages, res.Messages...)
		}
		if res.TokenUsage != nil {
			if totalUsage == nil {
				totalUsage = &types.TokenUsage{}
			}
			totalUsage.PromptTokens += res.TokenUsage.PromptTokens
			totalUsage.CompletionTokens += res.TokenUsage.CompletionTokens
			totalUsage.TotalTokens += res.TokenUsage.TotalTokens
		}
		if i > 0 && res.Content != "" {
			content += "\n---\n"
		}
		content += "[" + agents[i].Name() + "] " + res.Content
	}

	return &kernel.Result{
		Content:    content,
		Messages:   combinedMessages,
		TokenUsage: totalUsage,
		Err:        firstErr,
	}
}

// ─── Chain ───────────────────────────────────────────────────────────────

// Chain executes Agents sequentially, passing the full message history
// from one Agent to the next. This is a Go pipeline pattern.
// Returns an Agent that blocks until the entire chain completes.
//
// Chain 串行执行多个 Agent，每个 Agent 接收前一个的完整消息历史作为输入。
// 这是 Go 流水线模式。返回一个阻塞直到全部链完成的 Agent。
func Chain(agents ...kernel.Agent) kernel.Agent {
	var names string
	for i, a := range agents {
		if i > 0 {
			names += "→"
		}
		names += a.Name()
	}
	return &chainAgent{
		agents: agents,
		name:   "chain:" + names,
	}
}

type chainAgent struct {
	agents []kernel.Agent
	name   string
}

// Name returns the chain agent's identifier.
// Name 返回链式 Agent 的标识。
func (c *chainAgent) Name() string        { return c.name }
// Description returns a description of the chain agent.
// Description 返回链式 Agent 的描述。
func (c *chainAgent) Description() string { return "Sequential chain of " + c.name }

// Run executes the sub-agents sequentially, passing the full message history
// from one agent to the next, and returns the last result.
// Run 按顺序执行子 Agent，将完整消息历史传给下一个，并返回最后的结果。
func (c *chainAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	if len(c.agents) == 0 {
		return &kernel.Result{}
	}

	currentInput := input
	var lastResult *kernel.Result

	for _, a := range c.agents {
		res := a.Run(ctx, currentInput, rt)
		if res.Err != nil {
			return res
		}
		lastResult = res
		// Pass all messages to the next agent
		currentInput = &types.AgentInput{
			Messages:        res.Messages,
			SystemPrompt:    currentInput.SystemPrompt,
			EnableStreaming: currentInput.EnableStreaming,
			MaxSteps:        currentInput.MaxSteps,
		}
	}

	return lastResult
}

// ─── Pipe ────────────────────────────────────────────────────────────────

// Pipe chains Agents with a transform function between each step.
// The transform converts the previous Agent's Result into the next Agent's Input.
//
// Pipe 在 Agent 链中插入转换函数，将前一个 Agent 的 Result 转换为下一个 Agent 的 Input。
func Pipe(agents []kernel.Agent, transform func(*kernel.Result) *types.AgentInput) kernel.Agent {
	var names string
	for i, a := range agents {
		if i > 0 {
			names += "|"
		}
		names += a.Name()
	}
	return &pipeAgent{
		agents:    agents,
		transform: transform,
		name:      "pipe:" + names,
	}
}

type pipeAgent struct {
	agents    []kernel.Agent
	transform func(*kernel.Result) *types.AgentInput
	name      string
}

// Name returns the pipe agent's identifier.
// Name 返回管道 Agent 的标识。
func (p *pipeAgent) Name() string        { return p.name }
// Description returns a description of the pipe agent.
// Description 返回管道 Agent 的描述。
func (p *pipeAgent) Description() string { return "Piped execution of " + p.name }

// Run executes the sub-agents in sequence, applying the transform between
// steps, and returns the last result.
// Run 依次执行子 Agent，在步骤之间应用转换函数，并返回最后的结果。
func (p *pipeAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	if len(p.agents) == 0 {
		return &kernel.Result{}
	}

	currentInput := input
	var lastResult *kernel.Result

	for _, a := range p.agents {
		res := a.Run(ctx, currentInput, rt)
		if res.Err != nil {
			return res
		}
		lastResult = res
		if p.transform != nil {
			currentInput = p.transform(res)
		} else {
			currentInput = &types.AgentInput{
				Messages:        res.Messages,
				SystemPrompt:    currentInput.SystemPrompt,
				EnableStreaming: currentInput.EnableStreaming,
				MaxSteps:        currentInput.MaxSteps,
			}
		}
	}

	return lastResult
}

// ─── ExecToolsConcurrent ─────────────────────────────────────────────────

// ToolExecOption configures tool execution behavior.
//
// ToolExecOption 配置工具执行行为。
type ToolExecOption func(*toolExecConfig)

type toolExecConfig struct {
	maxConcurrency int
	callTimeout    func(toolName string) time.Duration
}

// WithToolConcurrency limits concurrent tool executions.
// Default: all tools run concurrently.
//
// WithToolConcurrency 限制最大并发工具执行数。默认全部并发执行。
func WithToolConcurrency(n int) ToolExecOption {
	return func(c *toolExecConfig) {
		c.maxConcurrency = n
	}
}

// WithCallTimeout supplies the per-call cooperative execution budget by
// tool name: a call whose tool resolves a positive duration runs under a
// deadline derived from the batch context; the budget is the tool's own
// declaration, enforced here (DSH tool-call-timeout separation). A tool
// that ignores its context cancellation will not stop on timeout — only
// signal-forwarding tools should declare a budget. The resolver returning
// 0 leaves the call under the batch context alone.
//
// WithCallTimeout 按工具名提供每次调用的协作式执行预算：解析到正时长
// 的调用在派生自批次上下文的截止时间内运行；预算由工具自身声明、在此
// 强制（DSH 工具超时分离）。忽略 context 取消的工具不会在超时后停止——
// 只有转发信号的工具应声明预算。解析器返回 0 时调用仅在批次上下文下
// 运行。
func WithCallTimeout(resolver func(toolName string) time.Duration) ToolExecOption {
	return func(c *toolExecConfig) {
		c.callTimeout = resolver
	}
}

// ToolExecResult carries one tool call's outcome: the tool message (always
// non-nil for executed calls) and the underlying execution error, if any.
// Failed calls embed the error in Message.Content as {"error": ...} to keep
// the message stream self-contained.
//
// ToolExecResult 携带一次工具调用的结果：工具消息与底层执行错误。
// 失败调用的错误同时以 {"error": ...} 形式嵌入 Message.Content。
type ToolExecResult struct {
	Message *types.Message
	Err     error
}

// ExecToolsConcurrentResults executes tool calls concurrently and returns
// each call's message together with its execution error, so callers can
// distinguish a real execution failure from a tool that returned an error
// payload. Independent calls run in parallel (semaphore-controlled).
//
// ExecToolsConcurrentResults 并发执行工具调用，并返回每条调用的错误明细，
// 供调用方区分"执行失败"与"工具返回错误内容"。
func ExecToolsConcurrentResults(ctx context.Context, tools []*types.ToolCall, registry kernel.ToolRegistry, opts ...ToolExecOption) []ToolExecResult {
	if len(tools) == 0 {
		return nil
	}

	cfg := &toolExecConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	maxConc := len(tools)
	if cfg.maxConcurrency > 0 && cfg.maxConcurrency < maxConc {
		maxConc = cfg.maxConcurrency
	}

	results := make([]ToolExecResult, len(tools))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup

	for i := range tools {
		wg.Add(1)
		sem <- struct{}{}
		i := i
		tc := tools[i]
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				// A panicking tool must not crash the host: degrade to an
				// error payload message (C3).
				if r := recover(); r != nil {
					tcName, tcID := "", ""
					if tc != nil {
						tcName, tcID = tc.Function.Name, tc.ID
					}
					results[i] = ToolExecResult{
						Message: types.NewToolMessage(jsonErrorPayload("tool panicked: "+fmt.Sprint(r)), tcID, tcName),
						Err:     fmt.Errorf("tool %q panicked: %v", tcName, r),
					}
				}
			}()

			// Public API input guards: a nil call or a nil registry must
			// degrade to an error payload, never panic (C-series).
			if tc == nil {
				results[i] = ToolExecResult{
					Message: types.NewToolMessage(jsonErrorPayload("tool call is nil"), "", ""),
					Err:     errors.New("tool call is nil"),
				}
				return
			}
			kernel.NormalizeToolCall(tc)
			if registry == nil {
				results[i] = ToolExecResult{
					Message: types.NewToolMessage(jsonErrorPayload("no tool registry"), tc.ID, tc.Function.Name),
					Err:     errors.New("no tool registry"),
				}
				return
			}
			tool := registry.Get(ctx, tc.Function.Name)
			if tool == nil {
				results[i] = ToolExecResult{
					Message: types.NewToolMessage(jsonErrorPayload("tool not found: "+tc.Function.Name), tc.ID, tc.Function.Name),
					Err:     fmt.Errorf("tool not found: %s", tc.Function.Name),
				}
				return
			}

			// Per-call declared budget: a deadline derived from the batch
			// context, enforced before dispatch. A timeout surfaces as a
			// structured TOOL_TIMEOUT error so the caller distinguishes a
			// budget exhaustion from a real failure.
			callCtx := ctx
			if cfg.callTimeout != nil {
				if d := cfg.callTimeout(tc.Function.Name); d > 0 {
					var cancel context.CancelFunc
					callCtx, cancel = context.WithTimeout(ctx, d)
					defer cancel()
				}
			}
			result, parts, err := runToolResult(callCtx, tool, tc.Function.Arguments)
			if callCtx.Err() == context.DeadlineExceeded {
				results[i] = ToolExecResult{
					Message: types.NewToolMessage(jsonErrorPayload("tool call timed out",
						map[string]any{"code": "TOOL_TIMEOUT", "tool": tc.Function.Name}), tc.ID, tc.Function.Name),
					Err: fmt.Errorf("tool %s: %w", tc.Function.Name, context.DeadlineExceeded),
				}
				return
			}
			if err != nil {
				results[i] = ToolExecResult{
					Message: types.NewToolMessage(jsonErrorPayload(err.Error()), tc.ID, tc.Function.Name),
					Err:     err,
				}
				return
			}
			results[i] = ToolExecResult{
				Message: newToolResultMessage(result, parts, tc.ID, tc.Function.Name),
			}
		}()
	}

	wg.Wait()
	return results
}

// jsonErrorPayload builds a JSON-safe {"error": ...} tool-message payload;
// extra fields (code/tool) may be supplied. Raw error text must never break
// the message JSON fed back to the LLM provider (C6).
func jsonErrorPayload(msg string, extra ...map[string]any) string {
	m := map[string]any{"error": msg}
	if len(extra) > 0 {
		for k, v := range extra[0] {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// runToolResult runs a tool, honoring the optional ToolWithResult capability.
// Tools that only implement Tool return a nil parts slice — the zero value
// that keeps the text-only path unchanged.
//
// runToolResult 执行工具，支持可选的 ToolWithResult 能力。仅实现 Tool
// 的工具返回 nil 片段——该零值保持纯文本路径不变。
func runToolResult(ctx context.Context, t kernel.Tool, argsJSON string) (string, []types.ContentPart, error) {
	if twr, ok := t.(kernel.ToolWithResult); ok {
		return twr.RunWithResult(ctx, argsJSON)
	}
	s, err := t.Run(ctx, argsJSON)
	return s, nil, err
}

// newToolResultMessage builds a tool result message, attaching multimodal
// content parts when present. A nil parts slice preserves the legacy
// text-only message.
//
// newToolResultMessage 构造工具结果消息，存在多模态片段时一并附加。
// nil 片段保持纯文本消息不变。
func newToolResultMessage(content string, parts []types.ContentPart, toolCallID, toolName string) *types.Message {
	msg := types.NewToolMessage(content, toolCallID, toolName)
	if len(parts) > 0 {
		msg.ContentParts = parts
	}
	return msg
}

// ExecToolsConcurrent executes a set of tool calls concurrently, returning
// only the result messages. Independent tool calls run in parallel;
// results are collected and returned as messages. See
// ExecToolsConcurrentResults for the variant that also reports per-call
// execution errors.
//
// ExecToolsConcurrent 并发执行一组工具调用，仅返回结果消息。
// 不依赖的工具调用并行执行。需要错误明细时请使用 ExecToolsConcurrentResults。
func ExecToolsConcurrent(ctx context.Context, tools []*types.ToolCall, registry kernel.ToolRegistry, opts ...ToolExecOption) []*types.Message {
	if len(tools) == 0 {
		return nil
	}
	results := ExecToolsConcurrentResults(ctx, tools, registry, opts...)
	msgs := make([]*types.Message, len(results))
	for i, r := range results {
		msgs[i] = r.Message
	}
	return msgs
}
