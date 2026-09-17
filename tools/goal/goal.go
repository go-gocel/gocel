// Package goal exposes the durable-goal manager to the model: create_goal,
// get_goal, and update_goal. The authority model mirrors DSH: host
// operations (create, pause/resume, complete, clear, edit) require the
// host's initiative, while a goal round may only continue an existing
// blocker past the streak threshold — a round can never block or complete a
// fresh goal by itself.
//
// Host-turn detection reads the shared run state key "goal:host_turn",
// written by module/goalround (true for host-initiated runs, false for
// auto-continuation rounds). Without the module installed the key is absent
// and host operations fail closed; products that do not run the round
// driver may override HostTurn.
package goal

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-gocel/gocel/core/goal"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
)

// HostTurnKey is the run-state key module/goalround writes to mark
// host-initiated runs. Consumers keep it in sync when they set it
// themselves.
// HostTurnKey 是 module/goalround 写入的运行状态键，用于标记主机发起的
// 运行；自行设置该键的消费方需保持同步。
const HostTurnKey = "goal:host_turn"

// Config wires the tool set to a manager.
// Config 将工具集连接到 goal 管理器。
type Config struct {
	// Manager is the goal manager the tools operate on.
	Manager *goal.Manager
	// HostTurn reports whether the current execution is host-initiated.
	// Nil reads the shared state key HostTurnKey; an absent key fails
	// closed (no host authority). Products not running module/goalround
	// may supply their own predicate.
	HostTurn func(ctx context.Context) bool
	// BlockThreshold is the autonomous-block streak threshold (DSH
	// blockedAfterConsecutiveRounds); default 3.
	BlockThreshold int
}

// Tools builds create_goal / get_goal / update_goal.
// Tools 构建 create_goal / get_goal / update_goal 三个工具。
func Tools(cfg Config) ([]kernel.Tool, error) {
	if cfg.Manager == nil {
		return nil, fmt.Errorf("goal tools: nil manager")
	}
	if cfg.HostTurn == nil {
		cfg.HostTurn = hostTurnFromState
	}
	if cfg.BlockThreshold == 0 {
		cfg.BlockThreshold = 3
	}
	c := &client{cfg: cfg}

	create, err := tool.ToolFromFunc(
		c.create,
		tool.WithToolName("create_goal"),
		tool.WithToolDescription("Create a durable session goal the harness will work toward across rounds. Returns the goal with its id, phase, and revision."),
		tool.WithArgNames("objective", "max_rounds"),
		tool.WithArgDescs("The concrete completion objective", "Optional cap on auto-continuation rounds (0 = unlimited)"),
	)
	if err != nil {
		return nil, err
	}
	get, err := tool.ToolFromFunc(
		c.get,
		tool.WithToolName("get_goal"),
		tool.WithToolDescription("Read one goal by id (phase, revision, rounds, blocker)."),
		tool.WithArgNames("id"),
		tool.WithArgDescs("The goal id"),
	)
	if err != nil {
		return nil, err
	}
	update, err := tool.ToolFromFunc(
		c.update,
		tool.WithToolName("update_goal"),
		tool.WithToolDescription("Update a goal: action=pause|resume|complete|block|clear, or edit the objective. pause/resume/complete/clear/edit require a host turn; block continues an existing blocker (autonomous rounds may only re-report the same blocker after it persisted). reason is required for block; objective is required for edit."),
	)
	if err != nil {
		return nil, err
	}
	return []kernel.Tool{create, get, update}, nil
}

// updateArgs is the flattened, partially-optional argument set of
// update_goal: only id and action are required; reason serves block and
// objective serves edit (FuncTool derives the schema from the json tags).
type updateArgs struct {
	ID        string `json:"id" description:"The goal id"`
	Action    string `json:"action" description:"One of pause/resume/complete/block/clear/edit"`
	Reason    string `json:"reason,omitempty" description:"Blocker reason; required for block"`
	Objective string `json:"objective,omitempty" description:"New objective; required for edit"`
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

// hostTurnFromState is the default host-turn source: the shared run state,
// fail-closed when absent.
func hostTurnFromState(ctx context.Context) bool {
	ac := kernel.GetAgentContext(ctx)
	if ac == nil || ac.State() == nil {
		return false
	}
	v, ok := ac.State().Get(HostTurnKey)
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

type client struct {
	cfg Config
}

func marshalGoal(g *goal.Goal) string {
	b, _ := json.Marshal(g)
	return string(b)
}

func (c *client) create(ctx context.Context, objective string, maxRounds int) (string, error) {
	if !c.cfg.HostTurn(ctx) {
		return "", fmt.Errorf("goal: create requires a host turn")
	}
	g, err := c.cfg.Manager.Create(ctx, objective, maxRounds)
	if err != nil {
		return "", err
	}
	return marshalGoal(g), nil
}

func (c *client) get(ctx context.Context, id string) (string, error) {
	g, err := c.cfg.Manager.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return marshalGoal(g), nil
}

func (c *client) update(ctx context.Context, args updateArgs) (string, error) {
	m := c.cfg.Manager
	host := c.cfg.HostTurn(ctx)
	switch args.Action {
	case "pause":
		if !host {
			return "", fmt.Errorf("goal: pause requires a host turn")
		}
		g, err := m.Pause(ctx, args.ID)
		return updated(g, err)
	case "resume":
		if !host {
			return "", fmt.Errorf("goal: resume requires a host turn")
		}
		g, err := m.Resume(ctx, args.ID)
		return updated(g, err)
	case "complete":
		if !host {
			return "", fmt.Errorf("goal: complete requires a host turn")
		}
		g, err := m.Complete(ctx, args.ID)
		return updated(g, err)
	case "clear":
		if !host {
			return "", fmt.Errorf("goal: clear requires a host turn")
		}
		if err := m.Clear(ctx, args.ID); err != nil {
			return "", err
		}
		return `{"cleared":true}`, nil
	case "block":
		if host {
			g, err := m.Block(ctx, args.ID, args.Reason)
			return updated(g, err)
		}
		g, err := m.Block(ctx, args.ID, args.Reason, goal.WithAutonomousBlock(c.cfg.BlockThreshold))
		return updated(g, err)
	case "edit":
		if !host {
			return "", fmt.Errorf("goal: edit requires a host turn")
		}
		g, err := m.Update(ctx, args.ID, func(g *goal.Goal) error {
			if args.Objective == "" {
				return goal.ErrInvalidObjective
			}
			g.Objective = args.Objective
			return nil
		})
		return updated(g, err)
	default:
		return "", fmt.Errorf("goal: unknown action %q (want pause/resume/complete/block/clear/edit)", args.Action)
	}
}

func updated(g *goal.Goal, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return marshalGoal(g), nil
}
