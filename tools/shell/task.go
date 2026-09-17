// Background task tools: task_run starts a long-running command in the
// background, task_status polls its state and recent output, task_stop
// terminates it. Together they cover hour-scale work (big builds, data
// migrations, load tests) that exceeds the synchronous terminal timeout cap.
//
// TaskRunner is the shell facade over the framework-level jobs registry
// (core/jobs): tasks are jobs with kind "task", fenced by the session id,
// with bounded stored output. A consumer may share its own registry
// (NewTaskRunnerWithRegistry) so every background job — shell or otherwise
// — is observable through one surface (tools/jobs).
package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/jobs"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
)

const (
	// taskLogCap bounds the stored task log (oldest bytes dropped; the
	// tail — recent progress — is what pollers care about).
	taskLogCap = 1 << 20 // 1MB
	// taskTailCap is the log tail returned by task_status.
	taskTailCap = 8000
)

// TaskRunner is the shared registry behind task_run/task_status/task_stop.
// Tasks are not bound to the agent turn context: they keep running after the
// tool call returns and stop via task_stop or process exit.
// TaskRunner 是 task_run/task_status/task_stop 背后的共享注册表。任务不
// 绑定 agent 回合上下文：工具调用返回后仍继续运行，经 task_stop 或进程
// 退出而停止。
type TaskRunner struct {
	// LogDir is the log directory relative to the shell work directory
	// (e.g. "tasks"); empty disables file logging.
	LogDir string

	// ShellName/ShellArgs override the default "sh -c" execution shell.
	// Background jobs run on a registry-derived context rooted at
	// context.Background() (the job must outlive the agent turn), so the
	// per-call context injection (WithShellInContext) does NOT reach task
	// execution — hosts without sh (bash-only containers, Windows without
	// git-bash) must set these to a resolved shell.
	//
	// ShellName/ShellArgs 覆盖默认 "sh -c" 执行 shell。后台任务运行在
	// 以 context.Background() 为根的注册表上下文上（任务须存活于 agent
	// 回合之后），调用方 ctx 的 WithShellInContext 注入不会到达任务
	// 执行——无 sh 的环境（bash-only 容器、无 Git Bash 的 Windows）
	// 必须在此设置探测到的 shell。
	ShellName string
	ShellArgs []string

	reg     *jobs.Registry
	mu      sync.Mutex
	next    int
	started map[string]time.Time // task id → start time (elapsed in status)
}

// NewTaskRunner creates a task registry with its own private jobs registry.
// logDir is the log directory relative to the work directory ("" = no file
// logs).
// NewTaskRunner 创建带私有 jobs 注册表的新任务运行器。logDir 是相对工作
// 目录的日志目录（"" = 不落文件日志）。
func NewTaskRunner(logDir string) *TaskRunner {
	return NewTaskRunnerWithRegistry(jobs.NewRegistry(), logDir)
}

// NewTaskRunnerWithRegistry creates a task runner on a shared jobs registry,
// so the tasks join every other background job on one observation surface.
// NewTaskRunnerWithRegistry 在共享 jobs 注册表上创建任务运行器，使任务与
// 其他所有后台作业汇入同一个可观测表面。
func NewTaskRunnerWithRegistry(reg *jobs.Registry, logDir string) *TaskRunner {
	return &TaskRunner{LogDir: logDir, reg: reg, started: make(map[string]time.Time)}
}

// Registry exposes the underlying jobs registry (e.g. for tools/jobs).
// Registry 暴露底层 jobs 注册表（例如供 tools/jobs 使用）。
func (r *TaskRunner) Registry() *jobs.Registry { return r.reg }

// taskRunArgs is the task_run tool schema.
type taskRunArgs struct {
	Command string `json:"command" description:"Shell command to run in the background."`
}

// TaskRunTool returns the task_run tool: start a long command in the
// background and return immediately with a task ID.
// TaskRunTool 返回 task_run 工具：在后台启动长命令并立即返回任务 ID。
func TaskRunTool(r *TaskRunner) kernel.Tool {
	return tool.MustToolFromFunc(
		func(ctx context.Context, args taskRunArgs) (string, error) {
			command := strings.TrimSpace(args.Command)
			if command == "" {
				return "", fmt.Errorf("task_run: command is required")
			}
			return r.start(ctx, command)
		},
		tool.WithToolName("task_run"),
		tool.WithToolDescription("Start a long-running shell command in the background (big builds, migrations, load tests). Returns a task ID immediately; poll with task_status, stop with task_stop."),
	)
}

// taskStatusArgs is the task_status tool schema.
type taskStatusArgs struct {
	ID string `json:"id" description:"Task ID returned by task_run."`
}

// TaskStatusTool returns the task_status tool: poll a background task.
// TaskStatusTool 返回 task_status 工具：轮询后台任务。
func TaskStatusTool(r *TaskRunner) kernel.Tool {
	return tool.MustToolFromFunc(
		func(ctx context.Context, args taskStatusArgs) (string, error) {
			return r.status(strings.TrimSpace(args.ID))
		},
		tool.WithToolName("task_status"),
		tool.WithToolDescription("Query a task_run task: state (running/done/failed/stopped), elapsed time, recent (tail) output. Poll every ~5s+."),
	)
}

// taskStopArgs is the task_stop tool schema.
type taskStopArgs struct {
	ID string `json:"id" description:"Task ID returned by task_run."`
}

// TaskStopTool returns the task_stop tool: terminate a background task.
// TaskStopTool 返回 task_stop 工具：终止后台任务。
func TaskStopTool(r *TaskRunner) kernel.Tool {
	return tool.MustToolFromFunc(
		func(ctx context.Context, args taskStopArgs) (string, error) {
			return r.stop(strings.TrimSpace(args.ID))
		},
		tool.WithToolName("task_stop"),
		tool.WithToolDescription("Stop a task_run task (terminates the process; finished tasks are left as-is)."),
	)
}

// start launches the command detached from the call context (background
// semantics): the agent turn may end while the task keeps running.
func (r *TaskRunner) start(ctx context.Context, command string) (string, error) {
	r.mu.Lock()
	r.next++
	id := fmt.Sprintf("t%d", r.next)
	r.mu.Unlock()

	wd := workDir(ctx)
	logPath := ""
	if r.LogDir != "" {
		logPath = filepath.Join(wd, r.LogDir, id+".log")
	}
	owner := ""
	if ac := kernel.GetAgentContext(ctx); ac != nil {
		owner = ac.Facts().SessionID
	}
	r.mu.Lock()
	r.started[id] = time.Now()
	r.mu.Unlock()

	_, err := r.reg.Start(jobs.Spec{
		ID:            id,
		Kind:          "task",
		Label:         command,
		Owner:         owner,
		StoreCapBytes: taskLogCap,
		Run: func(bgCtx context.Context, w io.Writer) (string, error) {
			// Open the audit log once per task (append mode); per-write
			// open/close would thrash the filesystem on heavy output.
			var out io.Writer = w
			if logPath != "" {
				if dir := filepath.Dir(logPath); dir != "." {
					_ = os.MkdirAll(dir, 0o755)
				}
				file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
				if err != nil {
					return "", fmt.Errorf("task_run: open log: %w", err)
				}
				defer file.Close()
				out = io.MultiWriter(w, file)
			}
			// Background ctx does not carry caller values (registry derives
			// it from context.Background()), so the runner-level shell
			// config is authoritative; the context injection is kept as a
			// fallback for registries that do propagate values.
			spec := resolveShell(bgCtx, nil)
			if r.ShellName != "" {
				spec.name = r.ShellName
				spec.args = append([]string{}, r.ShellArgs...)
				if len(spec.args) == 0 {
					spec.args = []string{"-c"}
				}
			}
			cmd := exec.CommandContext(bgCtx, spec.name, append(append([]string{}, spec.args...), command)...)
			cmd.Dir = wd
			// Same pipe-inheritance guard as terminal: after task_stop the
			// Wait must not hang on grandchild-held pipes.
			cmd.WaitDelay = waitDelay
			cmd.Stdout = out
			cmd.Stderr = out
			if err := cmd.Start(); err != nil {
				return "", fmt.Errorf("task_run: start failed: %w", err)
			}
			return "", cmd.Wait()
		},
	})
	if err != nil {
		return "", fmt.Errorf("task_run: %v", err)
	}
	return fmt.Sprintf("Task %s started in the background%s. Poll it with task_status; stop it with task_stop.",
		id, logHint(logPath)), nil
}

func logHint(path string) string {
	if path == "" {
		return ""
	}
	return fmt.Sprintf(" (log: %s)", path)
}

// stateText maps the registry status to the task tools' legacy vocabulary:
// stopping presents as stopped (the user-visible intent; the registry stays
// honest internally).
func stateText(s jobs.Status) string {
	switch s {
	case jobs.StatusRunning:
		return "running"
	case jobs.StatusStopping, jobs.StatusKilled:
		return "stopped"
	case jobs.StatusCompleted:
		return "done"
	case jobs.StatusFailed:
		return "failed"
	default:
		return string(s)
	}
}

// status reports state and the log tail.
func (r *TaskRunner) status(id string) (string, error) {
	job, err := r.reg.Status(id)
	if err != nil {
		return "", fmt.Errorf("task_status: unknown task %q (start one with task_run first)", id)
	}
	state := stateText(job.Status)
	var b strings.Builder
	r.mu.Lock()
	started, hasStart := r.started[id]
	r.mu.Unlock()
	b.WriteString(fmt.Sprintf("Task %s: %s", id, state))
	if hasStart {
		b.WriteString(fmt.Sprintf(", running for %s", time.Since(started).Round(time.Second)))
	}
	if logDir := r.LogDir; logDir != "" {
		b.WriteString(fmt.Sprintf("\nLog: %s", filepath.Join(workDirFromJob(), logDir, id+".log")))
	}
	out, _, err := r.reg.Output(id, 0)
	if err != nil {
		return "", fmt.Errorf("task_status: %w", err)
	}
	tail := string(out)
	if len(tail) > taskTailCap {
		tail = tail[len(tail)-taskTailCap:]
	}
	b.WriteString("\n")
	if tail == "" {
		b.WriteString("(no output yet)")
	} else {
		b.WriteString(fmt.Sprintf("Recent output (tail %d bytes):\n%s", len(tail), tail))
	}
	return b.String(), nil
}

// stop cancels the background context (killing the process) and marks the
// task stopped.
func (r *TaskRunner) stop(id string) (string, error) {
	job, err := r.reg.Status(id)
	if err != nil {
		return "", fmt.Errorf("task_stop: unknown task %q (start one with task_run first)", id)
	}
	if job.Status != jobs.StatusRunning && job.Status != jobs.StatusStopping {
		return fmt.Sprintf("Task %s already finished (%s); nothing to stop", id, stateText(job.Status)), nil
	}
	if err := r.reg.Kill(id); err != nil {
		return "", fmt.Errorf("task_stop: %w", err)
	}
	return fmt.Sprintf("Task %s stopped", id), nil
}

// workDirFromJob returns the process cwd (the shell work dir default); the
// registry does not store the per-task work dir, and the log path hint uses
// the relative directory.
func workDirFromJob() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
