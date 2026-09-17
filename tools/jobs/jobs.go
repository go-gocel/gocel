// Package jobs exposes the background-job registry to the model: job_list,
// job_output (single-cursor incremental reads), and job_kill. Any
// long-running work registered in the shared registry — shell tasks,
// subagents, workflows — is observable through this one surface.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-gocel/gocel/core/jobs"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
)

// Config wires the tools to a registry.
// Config 将工具连接到后台任务注册表。
type Config struct {
	Registry *jobs.Registry
}

// Tools builds job_list / job_output / job_kill / job_wait.
// Tools 构建 job_list / job_output / job_kill / job_wait 四个工具。
func Tools(cfg Config) ([]kernel.Tool, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("job tools: nil registry")
	}
	c := &client{cfg: cfg}

	list, err := tool.ToolFromFunc(
		c.list,
		tool.WithToolName("job_list"),
		tool.WithToolDescription("List background jobs (id, kind, label, status); optional owner session filter."),
	)
	if err != nil {
		return nil, err
	}
	output, err := tool.ToolFromFunc(
		c.output,
		tool.WithToolName("job_output"),
		tool.WithToolDescription("Read a background job's output incrementally: pass the offset from the previous read (0 initially); returns the new bytes and the new offset. Also returns the job status."),
	)
	if err != nil {
		return nil, err
	}
	kill, err := tool.ToolFromFunc(
		c.kill,
		tool.WithToolName("job_kill"),
		tool.WithToolDescription("Terminate a background job (the job settles as killed)."),
	)
	if err != nil {
		return nil, err
	}
	wait, err := tool.ToolFromFunc(
		c.wait,
		tool.WithToolName("job_wait"),
		tool.WithToolDescription("Block until a background job settles or the timeout expires; returns the settled snapshot (or the live snapshot at timeout)."),
	)
	if err != nil {
		return nil, err
	}
	return []kernel.Tool{list, output, kill, wait}, nil
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

// listArgs: owner is optional (empty = all jobs).
type listArgs struct {
	Owner string `json:"owner,omitempty" description:"Optional session id filter"`
}

func (c *client) list(_ context.Context, args listArgs) (string, error) {
	b, err := json.Marshal(c.cfg.Registry.List(args.Owner))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// outputArgs: id required; since is the previous offset (0 initially).
type outputArgs struct {
	ID    string `json:"id" description:"Job id"`
	Since int    `json:"since,omitempty" description:"Offset from the previous read (0 initially)"`
}

func (c *client) output(_ context.Context, args outputArgs) (string, error) {
	out, offset, err := c.cfg.Registry.Output(args.ID, args.Since)
	if err != nil {
		return "", err
	}
	job, err := c.cfg.Registry.Status(args.ID)
	if err != nil {
		return "", err
	}
	res := struct {
		Status string `json:"status"`
		Offset int    `json:"offset"`
		Output string `json:"output"`
	}{Status: string(job.Status), Offset: offset, Output: string(out)}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// killArgs: id required.
type killArgs struct {
	ID string `json:"id" description:"Job id"`
}

func (c *client) kill(_ context.Context, args killArgs) (string, error) {
	if err := c.cfg.Registry.Kill(args.ID); err != nil {
		return "", err
	}
	job, err := c.cfg.Registry.Status(args.ID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"id":%q,"status":%q}`, args.ID, string(job.Status)), nil
}

// waitArgs: id required, timeout_ms optional (0 = wait indefinitely).
type waitArgs struct {
	ID        string `json:"id" description:"Job id"`
	TimeoutMS int    `json:"timeout_ms,omitempty" description:"Max wait in milliseconds; 0 = wait until the job settles"`
}

func (c *client) wait(ctx context.Context, args waitArgs) (string, error) {
	var waitCtx context.Context
	var cancel context.CancelFunc
	if args.TimeoutMS > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
	} else {
		waitCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	job, err := c.cfg.Registry.Wait(waitCtx, args.ID)
	if err != nil {
		// Timeout/cancellation: report the live snapshot instead of an
		// error — the caller asked for a bounded wait, not a failure.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			live, statusErr := c.cfg.Registry.Status(args.ID)
			if statusErr == nil {
				return fmt.Sprintf(`{"id":%q,"status":%q,"timed_out":true}`, args.ID, string(live.Status)), nil
			}
		}
		return "", err
	}
	return fmt.Sprintf(`{"id":%q,"status":%q,"timed_out":false}`, job.ID, string(job.Status)), nil
}
