package kernel

import (
	"context"

	"github.com/go-gocel/gocel/core/types"
)

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// Agent is the assembled product contract of the Harness. There are two
// kinds of Agent:
//
//   - Strategy agents: Policy + Engine + tools + modules, assembled by the
//     harness builder. Prefer implementing kernel.Policy over this interface.
//   - Orchestration agents: composites that delegate to child agents
//     (router, graph, parallel, chain). They call child Agent.Run with the
//     same Runtime.
//
// Agent 是 Harness 的装配产物契约。有两种 Agent：
//   - 策略型：Policy + Engine + 工具 + 模块，由 harness 装配器组装。
//     定义策略请实现 kernel.Policy，而非本接口。
//   - 编排型：组合子 Agent（router、graph、parallel、chain），
//     用同一个 Runtime 调用子 Agent.Run。
type Agent interface {
	// Name returns the agent's identifier.
	// Name 返回 Agent 标识。
	Name() string

	// Description returns a description of the agent's capabilities.
	// Description 返回 Agent 能力描述。
	Description() string

	// Run executes the agent synchronously and returns *Result.
	// The Runtime provides model/tool access, state, and lifecycle hooks.
	// The Runtime also carries the injected AgentContext via context.
	//
	// Run 同步执行 Agent，返回 *Result。
	// Runtime 提供模型/工具访问、状态与生命周期钩子，
	// AgentContext 已由 Runner 注入 context。
	Run(ctx context.Context, input *types.AgentInput, rt Runtime) *Result
}

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// AgentSchemar is an optional interface that agents may implement to declare
// structured input/output schemas. Agents that do not need schema validation
// can omit this.
//
// AgentSchemar 是可选接口，Agent 可以实现它以声明结构化输入/输出 schema。
// 不需要 schema 校验的 Agent 可以省略此接口。
type AgentSchemar interface {
	InputSchema() map[string]any
	OutputSchema() map[string]any
}

// ToolNamer is an optional capability of an Agent. Agents that implement it
// declare an API-safe name ([a-zA-Z0-9_-]+) used when the agent is referenced
// in dynamically registered tool names, such as the supervisor's
// "transfer_to_<name>" delegation tools. LLM providers reject tool names
// outside this pattern, so undeclared names are slugified as a fallback.
//
// ToolNamer 是 Agent 的可选能力：声明一个 API 安全的名称
// （[a-zA-Z0-9_-]+），用于动态注册的工具名（如 supervisor 的
// transfer_to_<name> 委派工具）。未声明时框架会对名称做 slug 化兜底。
type ToolNamer interface {
	ToolName() string
}

// AgentOption configures an agent during construction.
//
// The option functions assert on EXPORTED setter methods (SetName,
// SetDescription, ...) — the former unexported setter assertions could
// never be satisfied outside package kernel, so every documented option
// silently no-oped for external types. Types that want the options apply
// the exported setters.
//
// AgentOption 用于在构造 Agent 时对其进行配置。选项函数断言目标实现了
// 导出的 setter 方法（SetName、SetDescription 等）——旧版断言未导出的
// setter，包外类型永远无法满足，使所有文档化的选项对外部类型静默失效。
// 希望使用选项的类型请实现对应的导出 setter。
type AgentOption func(any)

// WithName sets the agent's name.
// WithName 设置 Agent 的名称。
func WithName(name string) AgentOption {
	return func(a any) {
		if v, ok := a.(interface{ SetName(string) }); ok {
			v.SetName(name)
		}
	}
}

// WithDescription sets the agent's description.
// WithDescription 设置 Agent 的描述。
func WithDescription(desc string) AgentOption {
	return func(a any) {
		if v, ok := a.(interface{ SetDescription(string) }); ok {
			v.SetDescription(desc)
		}
	}
}

// WithSystemPrompt sets the system prompt for the agent.
// WithSystemPrompt 设置 Agent 的系统提示词。
func WithSystemPrompt(prompt string) AgentOption {
	return func(a any) {
		if v, ok := a.(interface{ SetSystemPrompt(string) }); ok {
			v.SetSystemPrompt(prompt)
		}
	}
}

// WithMaxSteps sets the maximum number of ReAct iterations.
// WithMaxSteps 设置最大 ReAct 迭代步数。
func WithMaxSteps(steps int) AgentOption {
	return func(a any) {
		if v, ok := a.(interface{ SetMaxSteps(int) }); ok {
			v.SetMaxSteps(steps)
		}
	}
}

// WithInputSchema sets the JSON Schema describing the expected input structure.
// WithInputSchema 设置描述预期输入结构的 JSON Schema。
func WithInputSchema(schema map[string]any) AgentOption {
	return func(a any) {
		if v, ok := a.(interface{ SetInputSchema(map[string]any) }); ok {
			v.SetInputSchema(schema)
		}
	}
}

// WithOutputSchema sets the JSON Schema describing the structured output.
// WithOutputSchema 设置描述结构化输出的 JSON Schema。
func WithOutputSchema(schema map[string]any) AgentOption {
	return func(a any) {
		if v, ok := a.(interface{ SetOutputSchema(map[string]any) }); ok {
			v.SetOutputSchema(schema)
		}
	}
}
