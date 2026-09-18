// Package agents is the public facade for assembling and running gocel agents.
//
// Two creation paths:
//   - New: fully customizable (name, description, system prompt, tools,
//     modules, engine).
//   - NewReactAgent: the ReAct loop built in, pre-seeded with a ReAct prompt.
//
// Both return a kernel.Agent; bind a model at run time with
// runner.NewRunner(agent, model).
//
// Package agents 是装配与运行 gocel agent 的对外门面。两条创建路径：
//   - New：完全自定义（名称、描述、系统提示词、工具、模块、引擎）。
//   - NewReactAgent：内置 ReAct 循环，并预置 ReAct 提示词。
//
// 两者都返回 kernel.Agent；运行时用 runner.NewRunner(agent, model) 绑定模型。
package agents

import (
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/engine"
)

// New builds a fully customizable agent. The ReAct StepLoop is the default
// engine; supply a custom core/engine.Engine via WithEngine to change the
// loop. Bind a model at run time with runner.NewRunner(agent, model).
//
// New 构建一个完全可定制的 Agent。默认引擎是 ReAct StepLoop；用 WithEngine
// 传入自定义 core/engine.Engine 可替换循环。运行时用
// runner.NewRunner(agent, model) 绑定模型。
func New(opts ...Option) kernel.Agent {
	cfg := &config{name: "agent"}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.engine == nil {
		cfg.engine = engine.New()
	}
	return &agent{
		name:         cfg.name,
		description:  cfg.description,
		systemPrompt: cfg.systemPrompt,
		tools:        cfg.tools,
		modules:      cfg.modules,
		engine:       cfg.engine,
		maxSteps:     cfg.maxSteps,
	}
}

// defaultReactPrompt is the built-in ReAct instruction set.
const defaultReactPrompt = `You are a helpful agent. Work through the task step by step:
1. Reason about what you need to do.
2. Call tools as needed to gather information or take actions.
3. Observe the tool results and continue.
4. Once the task is complete, give the final answer directly — do not call tools in the final answer.`

// NewReactAgent builds an agent with the built-in ReAct strategy: it loops
// while the model calls tools and finishes with the final answer. It
// pre-seeds a ReAct-oriented system prompt (overridable with WithSystemPrompt).
//
// NewReactAgent 构建一个自带 ReAct 策略的 Agent：模型调用工具时持续循环，
// 最终答案收尾。预置 ReAct 风格系统提示词（可用 WithSystemPrompt 覆盖）。
func NewReactAgent(opts ...Option) kernel.Agent {
	return New(append([]Option{
		WithName("react-agent"),
		WithSystemPrompt(defaultReactPrompt),
	}, opts...)...)
}
