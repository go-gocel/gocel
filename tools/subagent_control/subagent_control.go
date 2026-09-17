// Package subagent_control exposes the model-facing controls for live
// subagents: send_message (continue), interrupt_agent (stop the current
// turn only), and list_agents (the live tree).
package subagent_control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/orchestrate"
	"github.com/go-gocel/gocel/core/tool"
)

// Config wires the control tools to a registry.
//
// Config 把控制工具接到注册表上。
type Config struct {
	Registry *orchestrate.Registry
}

// Tools builds send_message / interrupt_agent / list_agents.
//
// Tools 构建 send_message / interrupt_agent / list_agents 三个控制工具。
func Tools(cfg Config) ([]kernel.Tool, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("subagent_control tools: nil registry")
	}
	c := &client{cfg: cfg}

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
	return []kernel.Tool{send, interrupt, list}, nil
}

// MustTools builds the tool set, panicking on configuration errors.
//
// MustTools 构建工具集，配置错误时直接 panic。
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
