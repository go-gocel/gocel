package graph

import (
	"context"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// NodeKind represents the type of a graph node.
// NodeKind 表示图节点的类型。
type NodeKind int

const (
	// NodeKindAgent executes a single agent.
	// NodeKindAgent 执行单个 Agent。
	NodeKindAgent NodeKind = iota

	// NodeKindChain executes agents sequentially (pipeline), chaining outputs to inputs.
	// NodeKindChain 顺序执行多个 Agent，输出链式传递到输入。
	NodeKindChain

	// NodeKindParallel executes agents concurrently and merges their outputs.
	// NodeKindParallel 并发执行多个 Agent，合并输出。
	NodeKindParallel

	// NodeKindCondition evaluates a ConditionFn to determine which outgoing edge to follow.
	// The ConditionFn must return a label matching one of the outgoing edge labels.
	//
	// NodeKindCondition 评估 ConditionFn 决定走哪条出边。
	// ConditionFn 必须返回与某条出边 label 匹配的值。
	NodeKindCondition

	// NodeKindPassthrough transforms the graph state without calling any LLM.
	// Useful for data preparation, validation, or post-processing.
	//
	// NodeKindPassthrough 变换 GraphState 但不调用 LLM。
	// 适用于数据准备、校验或后处理。
	NodeKindPassthrough
)

// ConditionFn determines which outgoing edge to follow.
// Returns a label that must match an edge's Label field.
// If no matching edge is found, execution stops with an error.
//
// ConditionFn 决定走哪条出边。返回值必须匹配某条边的 Label。
// 如果没有匹配的边，执行将报错停止。
type ConditionFn func(ctx ContextWithState, state *GraphState) (label string, err error)

// TransformFn transforms the graph state at a passthrough node.
// TransformFn 在透传节点变换 GraphState。
type TransformFn func(ctx ContextWithState, state *GraphState) error

// ContextWithState is a context.Context for graph node operations.
// Defined as an alias for clarity — graph node callbacks receive this type.
//
// ContextWithState 是图节点操作的上下文。定义为类型别名为清晰起见。
type ContextWithState = context.Context

// ── NodeOption ──────────────────────────────────────────────

// NodeOption configures a graph node.
// NodeOption 配置图节点。
type NodeOption func(*graphNode)

// WithDescription sets the node's description.
// WithDescription 设置节点描述。
func WithDescription(desc string) NodeOption {
	return func(n *graphNode) { n.description = desc }
}

// WithNodeTimeout sets the maximum execution duration for this node.
// 0 means no timeout.
//
// WithNodeTimeout 设置节点最大执行时长。0 表示无超时。
func WithNodeTimeout(d time.Duration) NodeOption {
	return func(n *graphNode) { n.timeout = d }
}

// WithNodeMaxRetries sets the number of retries on failure (default 0).
// WithNodeMaxRetries 设置失败重试次数（默认 0）。
func WithNodeMaxRetries(count int) NodeOption {
	return func(n *graphNode) { n.maxRetries = count }
}

// WithNodeRetryDelay sets the delay between retries (default 0).
// WithNodeRetryDelay 设置重试间隔（默认 0）。
func WithNodeRetryDelay(d time.Duration) NodeOption {
	return func(n *graphNode) { n.retryDelay = d }
}

// ── String ──────────────────────────────────────────────────

// String returns the human-readable name of the node kind.
// String 返回节点类型的可读名称。
func (k NodeKind) String() string {
	switch k {
	case NodeKindAgent:
		return "agent"
	case NodeKindChain:
		return "chain"
	case NodeKindParallel:
		return "parallel"
	case NodeKindCondition:
		return "condition"
	case NodeKindPassthrough:
		return "passthrough"
	default:
		return "unknown"
	}
}

// compile-time interface check
var _ kernel.Agent = (*graphAgentAdapter)(nil)

// graphAgentAdapter wraps a graph node as a kernel.Agent so it can be
// used wherever a single Agent is expected (e.g. nested in another graph).
//
// graphAgentAdapter 将图节点包装为 kernel.Agent，使其可以像普通 Agent 一样使用。
type graphAgentAdapter struct {
	cg   *CompiledGraph
	name string
}

// GraphAsAgent wraps a CompiledGraph as a kernel.Agent for nesting.
// GraphAsAgent 将 CompiledGraph 包装为 kernel.Agent，支持图嵌套。
func GraphAsAgent(cg *CompiledGraph, name string) kernel.Agent {
	return &graphAgentAdapter{cg: cg, name: name}
}

// Name returns the adapter's name.
// Name 返回适配器的名称。
func (a *graphAgentAdapter) Name() string        { return a.name }
// Description returns a description of the wrapped graph.
// Description 返回被包装图的描述。
func (a *graphAgentAdapter) Description() string { return "graph: " + a.cg.g.name }

// Run executes the wrapped CompiledGraph with the given input.
// Run 使用给定输入执行被包装的 CompiledGraph。
func (a *graphAgentAdapter) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	return a.cg.Run(ctx, input, rt)
}
