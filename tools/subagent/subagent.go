// Package subagent exposes the subagent registry to the model: the
// subagent / subagent_fork tools spawn background child sessions whose
// agent is built by the injected factory (the sole creator, mirroring the
// DSH registry semantics), running on the parent's runtime (model + tools);
// send_message / interrupt_agent / list_agents then control the live
// children. Spawning returns immediately with the child's handle; settlement
// arrives later as an EventNotice through the parent's event stream. Every
// tool in the set operates on the one registry the Config carries.
package subagent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// Config wires the tool set to a registry and a child-agent factory.
// Config 将工具集连接到注册表与子代理工厂。
type Config struct {
	// Registry is the subagent registry the tools operate on.
	Registry *orchestrate.Registry
	// Factory builds the child agent for one spawn. It is the sole creator
	// of child agents (DSH registry semantics). The tool-call context is
	// passed through, so the factory can read the caller's delegation depth
	// (see DepthFrom) and bake it into the child. The child runs on the
	// parent's runtime, captured from the tool-call context.
	Factory func(ctx context.Context) kernel.Agent
	// History, when non-nil, supplies the conversation history a fork
	// inherits; nil forks send only the prompt.
	History func(ctx context.Context) []*types.Message
	// MaxDepth caps the delegation tree depth (DSH depth floor); a spawn
	// beyond it is rejected. 0 = unlimited.
	MaxDepth int
	// DelegatedApproval pins an approval policy onto every child spawned
	// through these tools (DSH delegation policy inheritance: the child's
	// effective approval disposition is fixed at the delegation boundary).
	// Products that want unattended children pin permission.
	//NeverApprovalPolicy here — every ask is then rejected deterministically
	// instead of waiting on a prompt no one is watching. Nil leaves the
	// child's approval disposition to its own runtime configuration.
	//
	// DelegatedApproval 把审批策略钉在本工具集派生的每个子代理上（DSH
	// 委派策略继承：子代理的有效审批处置在委派边界固定）。需要无人值守
	// 子代理的产品在此钉 permission.NeverApprovalPolicy——一切询问被
	// 确定性拒绝，而不是等待一个无人观看的提示。nil 时子代理按自身
	// 运行时配置解析审批处置。
	DelegatedApproval kernel.ApprovalPolicy
}

type depthKey struct{}

// WithDepth returns ctx carrying the delegation depth for subagent tools.
// WithDepth 返回携带委派深度的 ctx，供 subagent 工具使用。
func WithDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, depthKey{}, depth)
}

// DepthFrom reads the delegation depth from ctx (0 at the root).
// DepthFrom 从 ctx 读取委派深度（根节点为 0）。
func DepthFrom(ctx context.Context) int {
	if d, ok := ctx.Value(depthKey{}).(int); ok {
		return d
	}
	return 0
}

// Tools builds the delegation tool set: subagent and subagent_fork spawn
// children, send_message / interrupt_agent / list_agents control them
// afterwards. All five share the Config's single registry.
// Tools 构建委派工具集：subagent 与 subagent_fork 负责派发，
// send_message / interrupt_agent / list_agents 负责后续控制，五者共用
// Config 的同一份注册表。
func Tools(cfg Config) ([]kernel.Tool, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("subagent tools: nil registry")
	}
	if cfg.Factory == nil {
		return nil, fmt.Errorf("subagent tools: nil factory")
	}
	c := &client{cfg: cfg}

	spawn, err := tool.ToolFromFunc(
		c.spawn,
		tool.WithToolName("subagent"),
		tool.WithToolDescription("Start a background child agent with a fresh context to work on a task. Returns the child handle immediately; the child reports completion through the notice event stream. Use send_message to continue the child, interrupt_agent to stop its current turn."),
		// Spawning performs no file mutation itself — the child runs under
		// the same runtime and its own calls go through the same gates, so
		// plan mode and read-only tiers still allow read-only delegation.
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	fork, err := tool.ToolFromFunc(
		c.fork,
		tool.WithToolName("subagent_fork"),
		tool.WithToolDescription("Start a background child agent that INHERITS this conversation's history, for branch exploration. Returns the child handle immediately; completion arrives through the notice event stream."),
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	send, err := tool.ToolFromFunc(
		c.sendMessage,
		tool.WithToolName("send_message"),
		tool.WithToolDescription("Send a follow-up message to a live background subagent. The message runs after the child's current turn finishes; use interrupt_agent to stop the current turn."),
		tool.WithArgNames("id", "message"),
		tool.WithArgDescs("The subagent id", "The follow-up message"),
	)
	if err != nil {
		return nil, err
	}
	interrupt, err := tool.ToolFromFunc(
		c.interrupt,
		tool.WithToolName("interrupt_agent"),
		tool.WithToolDescription("Stop a background subagent's CURRENT turn only — the child stays alive and continuable via send_message."),
		tool.WithArgNames("id"),
		tool.WithArgDescs("The subagent id"),
	)
	if err != nil {
		return nil, err
	}
	list, err := tool.ToolFromFunc(
		c.list,
		tool.WithToolName("list_agents"),
		tool.WithToolDescription("List the live background subagents (id, label, parent, status)."),
	)
	if err != nil {
		return nil, err
	}
	return []kernel.Tool{spawn, fork, send, interrupt, list}, nil
}

// spawnArgs is the argument set of subagent/subagent_fork: task is
// required, label is optional (FuncTool derives the schema from the json
// tags).
type spawnArgs struct {
	Task  string `json:"task" description:"The task prompt for the child agent"`
	Label string `json:"label,omitempty" description:"Optional short label for the child"`
}

// MustTools builds the tool set, panicking on configuration errors.
// MustTools 构建工具集，配置出错时 panic。
func MustTools(cfg Config) []kernel.Tool {
	ts, err := Tools(cfg)
	if err != nil {
		panic(err)
	}
	return ts
}

type client struct {
	cfg Config
}

func (c *client) spawn(ctx context.Context, args spawnArgs) (string, error) {
	if args.Task == "" {
		return "", fmt.Errorf("subagent: task is required")
	}
	return c.start(ctx, args.Label, []*types.Message{types.NewUserMessage(args.Task)})
}

func (c *client) fork(ctx context.Context, args spawnArgs) (string, error) {
	var history []*types.Message
	if c.cfg.History != nil {
		history = c.cfg.History(ctx)
	}
	task := args.Task
	if task == "" {
		// The fork inherits the conversation; an empty task means
		// "explore this branch further".
		task = "Continue exploring this branch."
	}
	// The forked child sees the inherited history followed by its task.
	msgs := make([]*types.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	msgs = append(msgs, types.NewUserMessage(task))
	return c.start(ctx, args.Label, msgs)
}

func (c *client) start(ctx context.Context, label string, msgs []*types.Message) (string, error) {
	if len(msgs) == 0 {
		return "", fmt.Errorf("subagent: no input messages")
	}
	if c.cfg.MaxDepth > 0 && DepthFrom(ctx) >= c.cfg.MaxDepth {
		return "", fmt.Errorf("subagent: delegation depth limit reached (%d)", c.cfg.MaxDepth)
	}
	parentID := ""
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		parentID = ac.Facts().SessionID
	}
	// Pin the delegation policy at the boundary before the registry captures
	// it (DSH delegation policy inheritance).
	if c.cfg.DelegatedApproval != nil {
		ctx = kernel.WithDelegatedApproval(ctx, c.cfg.DelegatedApproval)
	}
	sub, err := c.cfg.Registry.Spawn(ctx, parentID, label, c.cfg.Factory(ctx), &types.AgentInput{Messages: msgs})
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(sub)
	return string(b), nil
}

func (c *client) sendMessage(_ context.Context, id, message string) (string, error) {
	if err := c.cfg.Registry.Continue(context.Background(), id, message); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"queued":true,"id":%q}`, id), nil
}

func (c *client) interrupt(_ context.Context, id string) (string, error) {
	if err := c.cfg.Registry.Interrupt(context.Background(), id); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"interrupted":true,"id":%q}`, id), nil
}

func (c *client) list(_ context.Context) (string, error) {
	subs := c.cfg.Registry.List(context.Background())
	b, err := json.Marshal(subs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
