package agents

import (
	"context"

	coreengine "github.com/go-gocel/gocel/core/engine"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runner"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// Option configures an agent built by New or NewReactAgent.
// Option 配置由 New 或 NewReactAgent 构建的 Agent。
type Option func(*config)

type config struct {
	name         string
	description  string
	systemPrompt string
	tools        []kernel.Tool
	modules      []kernel.Module
	engine       coreengine.Engine
	maxSteps     int
}

// WithName sets the agent's name.
// WithName 设置 Agent 名称。
func WithName(name string) Option {
	return func(c *config) { c.name = name }
}

// WithDescription sets the agent's description.
// WithDescription 设置 Agent 描述。
func WithDescription(desc string) Option {
	return func(c *config) { c.description = desc }
}

// WithSystemPrompt sets the agent's system prompt.
// WithSystemPrompt 设置 Agent 的系统提示词。
func WithSystemPrompt(prompt string) Option {
	return func(c *config) { c.systemPrompt = prompt }
}

// WithTools appends tools available to the agent.
// WithTools 为 Agent 追加可用工具。
func WithTools(tools ...kernel.Tool) Option {
	return func(c *config) { c.tools = append(c.tools, tools...) }
}

// WithModules appends modules (lifecycle hooks) wired to the agent's runtime.
// WithModules 为 Agent 追加模块（生命周期钩子）。
func WithModules(modules ...kernel.Module) Option {
	return func(c *config) { c.modules = append(c.modules, modules...) }
}

// WithEngine replaces the agent's loop engine (default: the ReAct StepLoop).
// WithEngine 替换 Agent 的循环引擎（默认：ReAct StepLoop）。
func WithEngine(e coreengine.Engine) Option {
	return func(c *config) { c.engine = e }
}

// WithMaxSteps sets the agent's default step budget (overridden per run by
// AgentInput.MaxSteps when positive).
// WithMaxSteps 设置 Agent 默认步数预算（每次运行若 AgentInput.MaxSteps 为正则覆盖）。
func WithMaxSteps(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxSteps = n
		}
	}
}

// agent is the assembled strategy agent: identity + system prompt + engine +
// tools + modules. A model is bound at run time by the Runner.
//
// agent 是装配产物：身份 + 系统提示词 + 引擎 + 工具 + 模块。
// 模型在运行时由 Runner 绑定。
type agent struct {
	name         string
	description  string
	systemPrompt string
	tools        []kernel.Tool
	modules      []kernel.Module
	engine       coreengine.Engine
	maxSteps     int
}

var _ kernel.Agent = (*agent)(nil)
var _ runner.RunnerOptionProvider = (*agent)(nil)

// Name returns the agent's identifier.
// Name 返回 Agent 标识。
func (a *agent) Name() string { return a.name }

// Description returns the agent's description.
// Description 返回 Agent 描述。
func (a *agent) Description() string { return a.description }

// Run builds the message list, fires MessagesBuilt, and drives the engine.
// Run 构建消息列表、触发 MessagesBuilt 并驱动引擎。
func (a *agent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	msgs := a.buildMessages(input)

	var err error
	ctx, msgs, err = rt.FireMessagesBuilt(ctx, msgs)
	if err != nil {
		return &kernel.Result{Reason: kernel.TerminateError, Err: err}
	}

	maxSteps := a.maxSteps
	if input != nil && input.MaxSteps > 0 {
		maxSteps = input.MaxSteps
	}

	return a.engine.Run(ctx, &coreengine.RunInput{
		AgentName:  a.name,
		Messages:   msgs,
		MaxSteps:   maxSteps,
		StreamMode: input != nil && input.EnableStreaming,
		SendEvent:  resolveSend(ctx, input),
	}, rt)
}

// buildMessages assembles system (agent + input) and user messages. Multiple
// system messages are collapsed into one by FireMessagesBuilt.
func (a *agent) buildMessages(input *types.AgentInput) []*types.Message {
	msgs := make([]*types.Message, 0, 4)
	if a.systemPrompt != "" {
		msgs = append(msgs, types.NewSystemMessage(a.systemPrompt))
	}
	if input != nil {
		if input.SystemPrompt != "" {
			msgs = append(msgs, types.NewSystemMessage(input.SystemPrompt))
		}
		msgs = append(msgs, input.Messages...)
	}
	return msgs
}

// RunnerOptions returns the runner configuration (tools, modules) this agent
// carries, so NewRunner wires them on any construction path.
// RunnerOptions 返回本 Agent 携带的 Runner 配置（工具、模块），
// 使 NewRunner 在任何构建路径下都能接线。
func (a *agent) RunnerOptions() []runner.RunnerOption {
	var opts []runner.RunnerOption
	if len(a.tools) > 0 {
		opts = append(opts, runner.WithToolRegistry(tool.NewMapToolRegistry(a.tools)))
	}
	for _, m := range a.modules {
		opts = append(opts, runner.WithModule(m))
	}
	return opts
}

// resolveSend picks the event sender: the input's stream sender first, then
// the execution context's event channel.
func resolveSend(ctx context.Context, input *types.AgentInput) func(*types.Event) bool {
	if input != nil && input.StreamSender != nil {
		return input.StreamSender
	}
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		return ac.SendEvent()
	}
	return nil
}
