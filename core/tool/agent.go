package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

type agentTool struct {
	name        string
	description string
	agent       kernel.Agent
}

// NewAgentTool 从 Agent 构造工具，允许将子 Agent 暴露为可调用工具。
//
// 当 Agent 作为工具被调用时，LLM 传入的 JSON 参数会被转换为自然语言指令，
// 转发给子 Agent 执行，返回子 Agent 的输出文本。
//
// NewAgentTool wraps an Agent as a callable tool for LLM use.
//
//	coder := gocel.NewReActAgent(model)
//	tool.NewAgentTool("coder", "executes code analysis", coder)
func NewAgentTool(name, description string, agent kernel.Agent) kernel.Tool {
	return &agentTool{
		name:        name,
		description: description,
		agent:       agent,
	}
}

// MustAgentTool 类似 NewAgentTool，名称和描述从 Agent 自身的 Name()/Description() 获取。
//
// MustAgentTool is like NewAgentTool but derives name/description from the Agent itself.
func MustAgentTool(agent kernel.Agent) kernel.Tool {
	return NewAgentTool(agent.Name(), agent.Description(), agent)
}

// Name returns the agent tool's name.
// Name 返回代理工具的名称。
func (t *agentTool) Name() string                                       { return t.name }
// Description returns the agent tool's description.
// Description 返回代理工具的描述。
func (t *agentTool) Description() string                                { return t.description }
// ListTools returns the tool itself as the only entry.
// ListTools 返回只包含工具自身的列表。
func (t *agentTool) ListTools(_ context.Context) ([]kernel.Tool, error) { return []kernel.Tool{t}, nil }
// Schema returns the JSON Schema for the tool arguments (a free-form
// object).
// Schema 返回工具参数的 JSON Schema（自由形式对象）。
func (t *agentTool) Schema() map[string]any {
	return map[string]any{"type": "object"}
}
// ToolMeta returns the tool metadata with kind ToolKindAgent.
// ToolMeta 返回工具元数据，类型为 ToolKindAgent。
func (t *agentTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{Kind: kernel.ToolKindAgent}
}

// Run converts the JSON arguments into a natural-language instruction,
// runs the wrapped sub-agent, and returns its output text. It requires a
// kernel.Runtime in the context (set via kernel.WithRuntime).
// Run 将 JSON 参数转换为自然语言指令，运行被包装的子 Agent，并返回其
// 输出文本。上下文必须包含 kernel.Runtime（通过 kernel.WithRuntime 设置）。
func (t *agentTool) Run(ctx context.Context, argsJSON string) (string, error) {
	// Format argsJSON as readable text so the sub-agent understands the task
	userMsg := formatAgentToolInput(argsJSON)

	input := &types.AgentInput{
		Messages: []*types.Message{
			types.NewUserMessage(userMsg),
		},
	}

	rt := kernel.RuntimeFromContext(ctx)
	if rt == nil {
		return "", fmt.Errorf("agent tool %q: runtime not found in context; agent tools require a kernel.Runtime in context (set via kernel.WithRuntime)", t.name)
	}
	result := t.agent.Run(ctx, input, rt)
	if result.Err != nil {
		return result.Content, result.Err
	}
	return result.Content, nil
}

// formatAgentToolInput converts raw args JSON into a readable natural language prompt.
// If argsJSON is a JSON object, it extracts known "query"/"task"/"prompt" fields as
// the primary instruction and appends remaining fields as context.
// If parsing fails, returns the raw string (it may already be plain text).
func formatAgentToolInput(argsJSON string) string {
	var raw any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return argsJSON
	}

	obj, ok := raw.(map[string]any)
	if !ok {
		return argsJSON
	}

	// Check for known natural-language fields
	for _, key := range []string{"query", "task", "prompt", "question", "instruction"} {
		if v, exists := obj[key]; exists {
			if s, ok := v.(string); ok && s != "" {
				// Build a clean message: the main instruction plus remaining fields as context
				var sb strings.Builder
				sb.WriteString(s)
				remaining := make([]string, 0, len(obj)-1)
				for k, val := range obj {
					if k == key {
						continue
					}
					js, _ := json.Marshal(val)
					remaining = append(remaining, fmt.Sprintf("%s: %s", k, string(js)))
				}
				if len(remaining) > 0 {
					sb.WriteString("\n\nContext:\n")
					sb.WriteString(strings.Join(remaining, "\n"))
				}
				return sb.String()
			}
		}
	}

	// Fallback: pretty-print all fields as key-value pairs
	var sb strings.Builder
	sb.WriteString("Task details:\n")
	for k, val := range obj {
		js, _ := json.Marshal(val)
		sb.WriteString(fmt.Sprintf("  %s: %s\n", k, string(js)))
	}
	return sb.String()
}

// AgentToolOption 配置 AgentTool 的可选参数。
//
// AgentToolOption configures optional parameters for an AgentTool.
type AgentToolOption func(*agentTool)

// WithAgentToolName 自定义 Agent 工具的名称。
//
// WithAgentToolName sets a custom name for the agent tool.
func WithAgentToolName(name string) AgentToolOption {
	return func(t *agentTool) { t.name = name }
}

// WithAgentToolDescription 自定义 Agent 工具的描述。
//
// WithAgentToolDescription sets a custom description for the agent tool.
func WithAgentToolDescription(desc string) AgentToolOption {
	return func(t *agentTool) { t.description = desc }
}
