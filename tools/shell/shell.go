// Package shell provides shell execution tools for gocel agents.
package shell

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"

	"github.com/go-gocel/gocel/internal/toolutil"
)

const maxOutput = 8000

// progressChunkCap caps each streamed progress chunk (the model-facing
// output keeps its own 8K truncation; this only bounds UI event sizes).
const progressChunkCap = 8000

const (
	// DefaultTimeout is the per-command timeout used when the model declares
	// none. Ops/build/install workloads routinely exceed 30s; agents can
	// declare a longer timeout per call (see terminalArgs.Timeout).
	// DefaultTimeout 是模型未声明超时时使用的单命令超时。运维/构建/安装
	// 类任务经常超过 30s；代理可在每次调用时声明更长的超时（见
	// terminalArgs.Timeout）。
	DefaultTimeout = 30 * time.Second
	// MaxTimeout caps a declared timeout: hour-scale work belongs to the
	// background task tools (task_run/task_status/task_stop), not to a single
	// synchronous terminal call.
	// MaxTimeout 是声明超时的上限：小时级任务属于后台任务工具
	// （task_run/task_status/task_stop），而不是单次同步 terminal 调用。
	MaxTimeout = 10 * time.Minute
	// waitDelay bounds how long Wait blocks after cancellation when a
	// grandchild process still holds the output pipes (sh spawns children
	// that inherit the pipes; without WaitDelay Wait would hang until the
	// grandchild exits).
	waitDelay = 5 * time.Second
)

// config is the per-toolset shell configuration (AllTools options).
type config struct {
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	shellName      string
	shellArgs      []string
}

func defaultConfig() *config {
	return &config{defaultTimeout: DefaultTimeout, maxTimeout: MaxTimeout, shellName: "sh", shellArgs: []string{"-c"}}
}

// Option configures the shell toolset returned by AllTools.
// Option 配置 AllTools 返回的 shell 工具集。
type Option func(*config)

// WithDefaultTimeout sets the timeout used when the model declares none
// (e.g. ops agents want 60s, coding agents keep the 30s default).
// WithDefaultTimeout 设置模型未声明超时时的默认超时（例如运维代理想要
// 60s，编码代理保持 30s 默认值）。
func WithDefaultTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.defaultTimeout = d
		}
	}
}

// WithMaxTimeout caps a declared timeout; values above it are clamped.
// WithMaxTimeout 设置声明超时的上限；超过上限的值会被截断。
func WithMaxTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.maxTimeout = d
		}
	}
}

// WithShell overrides the shell used to execute commands (name plus the
// argument prefix that runs a command, e.g. "bash", "-c"). Defaults to
// "sh -c" — the POSIX baseline. Environments without sh (bash-only
// containers, Windows without Git Bash) should resolve a usable shell
// and inject it via WithShell or WithShellInContext.
//
// WithShell 覆盖执行命令所用的 shell（命令名 + 执行参数前缀，
// 如 "bash", "-c"）。默认 "sh -c"——POSIX 基线。没有 sh 的环境
// （bash-only 容器、无 Git Bash 的 Windows）应探测可用 shell 后
// 经 WithShell 或 WithShellInContext 注入。
func WithShell(name string, args ...string) Option {
	return func(c *config) {
		if strings.TrimSpace(name) == "" {
			return
		}
		c.shellName = name
		c.shellArgs = append([]string{}, args...)
		if len(c.shellArgs) == 0 {
			c.shellArgs = []string{"-c"}
		}
	}
}

// shellSpec is the resolved command prefix for shell execution.
type shellSpec struct {
	name string
	args []string
}

type shellSpecKey struct{}

// WithShellInContext injects the shell spec into context. When present it
// wins over the toolset-level WithShell option; both default to "sh -c".
// Tool calls may read the spec concurrently (commands run on multiple
// goroutines), so the spec is immutable.
//
// WithShellInContext 向 context 注入 shell 规格；存在时优先于工具集级
// WithShell 选项，两者都缺省回退 "sh -c"。
func WithShellInContext(ctx context.Context, name string, args ...string) context.Context {
	return context.WithValue(ctx, shellSpecKey{}, shellSpec{name: name, args: args})
}

// resolveShell returns the effective shell for a call: context injection,
// then the toolset option, then the "sh -c" default.
func resolveShell(ctx context.Context, cfg *config) shellSpec {
	if spec, ok := ctx.Value(shellSpecKey{}).(shellSpec); ok && spec.name != "" {
		if len(spec.args) == 0 {
			return shellSpec{name: spec.name, args: []string{"-c"}}
		}
		return spec
	}
	if cfg != nil && cfg.shellName != "" {
		return shellSpec{name: cfg.shellName, args: cfg.shellArgs}
	}
	return shellSpec{name: "sh", args: []string{"-c"}}
}

func workDir(ctx context.Context) string {
	if dir, ok := ctx.Value(workDirKey{}).(string); ok && dir != "" {
		return dir
	}
	return "."
}

type workDirKey struct{}

// WithWorkDir sets the working directory in context.
// WithWorkDir 在 context 中设置工作目录。
func WithWorkDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, workDirKey{}, dir)
}

// Progress is one incremental output chunk of a running shell command.
// Progress 是运行中的 shell 命令的一个增量输出块。
type Progress struct {
	// Phase is the stream the chunk came from ("stdout" or "stderr").
	Phase string
	// Text is the chunk content (may split mid-line; UI renders raw).
	Text string
}

type progressKey struct{}

// WithProgress injects a progress sink into context. When present, Terminal
// streams stdout/stderr chunks to the sink as the command runs (throttled to
// ~50ms, each chunk capped at 8K) while still collecting the full output for
// the model. Without a sink the behaviour is unchanged: one synchronous
// result. The sink may be invoked from the command's output goroutine and
// must be thread-safe.
// WithProgress 向 context 注入进度接收器；存在时 Terminal 在命令运行时把
// stdout/stderr 分块流式发送给接收器（约 50ms 节流，每块上限 8K），同时
// 仍为模型收集完整输出。没有接收器时行为不变：单次同步结果。接收器可能
// 从命令的输出 goroutine 调用，必须线程安全。
func WithProgress(ctx context.Context, sink func(Progress)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, sink)
}

func progressSink(ctx context.Context) func(Progress) {
	if fn, ok := ctx.Value(progressKey{}).(func(Progress)); ok && fn != nil {
		return fn
	}
	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	half := maxLen / 2
	return s[:half] + fmt.Sprintf("\n... [truncated %d chars] ...\n", len(s)-maxLen) + s[len(s)-half:]
}

// formatResult renders the model-facing tool output: the echoed command, the
// (8K-truncated, head+tail kept) combined output and the exit code on
// failure. Timeouts get a dedicated marker with guidance instead of a raw
// kill error.
func formatResult(command string, output []byte, err error, timeoutErr error) string {
	var result strings.Builder
	result.WriteString("$ " + command + "\n")
	outStr := string(output)
	if len(outStr) > maxOutput {
		outStr = truncate(outStr, maxOutput)
	}
	result.WriteString(outStr)
	if err != nil {
		if timeoutErr == context.DeadlineExceeded {
			result.WriteString(fmt.Sprintf("\n[timeout: command exceeded its declared timeout and was terminated. Declare a longer timeout argument (capped at %s) or use task_run for hour-scale work]", MaxTimeout))
		} else {
			result.WriteString(fmt.Sprintf("\n[exit code: %v]", err))
		}
	}
	return result.String()
}

// ── terminal ──

type terminalArgs struct {
	Command string `json:"command" description:"Shell command to execute."`
	Dir     string `json:"dir,omitempty" description:"Working directory relative to project root. Default: project root."`
	// Timeout is the per-command timeout ("90s", "5m"); empty = default.
	Timeout string `json:"timeout,omitempty" description:"Command timeout, e.g. \"90s\" or \"5m\". Default 30s, capped at 10m. Declare it for commands expected to run long (builds, installs, migrations); hour-scale tasks use task_run instead."`
}

// progressWriter collects the full output for the model while keeping the
// not-yet-sent delta for the progress sink. Safe for concurrent use: the
// command writes from its own goroutine, the throttler reads from another.
type progressWriter struct {
	mu      sync.Mutex
	total   bytes.Buffer
	pending bytes.Buffer
}

// Write appends the chunk to the collected output and the pending delta.
// Write 把数据块追加到已收集输出与待发送增量中。
func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total.Write(p)
	w.pending.Write(p)
	return len(p), nil
}

// flush returns the pending delta and resets it.
func (w *progressWriter) flush() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.pending.String()
	w.pending.Reset()
	return s
}

// output returns the complete collected output.
func (w *progressWriter) output() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.total.Bytes()
}

// streamOutput runs the command with a live progress sink: stdout/stderr are
// forwarded to the sink in throttled chunks while the full output is
// collected for the model-facing result.
func streamOutput(ctx context.Context, cmd *exec.Cmd, sink func(Progress)) ([]byte, error) {
	out := &progressWriter{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// The command is bound to a timeout context; cancellation kills
			// the process. WaitDelay (set by the caller) bounds how long Wait
			// may linger on grandchild-held pipes.
			err := <-done
			flushProgress(sink, out)
			return out.output(), err
		case err := <-done:
			flushProgress(sink, out)
			return out.output(), err
		case <-ticker.C:
			flushProgress(sink, out)
		}
	}
}

// flushProgress sends the pending delta to the sink, capped per chunk.
func flushProgress(sink func(Progress), out *progressWriter) {
	chunk := out.flush()
	if chunk == "" {
		return
	}
	if len(chunk) > progressChunkCap {
		chunk = truncate(chunk, progressChunkCap)
	}
	sink(Progress{Phase: "stdout", Text: chunk})
}

// resolveTimeout resolves the per-call timeout: empty = config default,
// non-positive = default, above the cap = clamped to the cap.
func resolveTimeout(declared string, cfg *config) (time.Duration, error) {
	if strings.TrimSpace(declared) == "" {
		return cfg.defaultTimeout, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(declared))
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q (use forms like \"90s\" or \"5m\")", declared)
	}
	if d <= 0 {
		return cfg.defaultTimeout, nil
	}
	if d > cfg.maxTimeout {
		d = cfg.maxTimeout
	}
	return d, nil
}

func terminalWithConfig(ctx context.Context, args terminalArgs, cfg *config) (string, error) {
	wd := workDir(ctx)
	cmdDir := wd
	if args.Dir != "" {
		cmdDir = filepath.Join(wd, args.Dir)
	}

	timeout, err := resolveTimeout(args.Timeout, cfg)
	if err != nil {
		return "", err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	spec := resolveShell(ctx, cfg)
	cmd := exec.CommandContext(timeoutCtx, spec.name, append(append([]string{}, spec.args...), args.Command)...)
	toolutil.HideWindow(cmd) // no cmd window flash on Windows desktop apps
	cmd.Dir = cmdDir
	// Grandchildren (sh → sleep, sh → pipelines) inherit the output pipes:
	// after cancellation, Wait must not hang until they exit on their own.
	cmd.WaitDelay = waitDelay

	var output []byte
	if sink := progressSink(ctx); sink != nil {
		output, err = streamOutput(timeoutCtx, cmd, sink)
	} else {
		// Default path: synchronous collection, unchanged behaviour.
		output, err = cmd.CombinedOutput()
	}
	return formatResult(args.Command, output, err, timeoutCtx.Err()), nil
}

// Terminal executes a shell command with the default timeout policy
// (DefaultTimeout / MaxTimeout). Kept as a plain function for direct callers
// and tests; AllTools exposes the option-based variant.
// Terminal 以默认超时策略（DefaultTimeout / MaxTimeout）执行 shell 命令。
// 保留为普通函数供直接调用方和测试使用；AllTools 暴露基于选项的变体。
func Terminal(ctx context.Context, args terminalArgs) (string, error) {
	return terminalWithConfig(ctx, args, defaultConfig())
}

// AllTools returns all shell tools (currently the parameterized terminal).
// The background task tools are provided separately via NewTaskRunner and
// TaskRunTool/TaskStatusTool/TaskStopTool so consumers opt into them.
// AllTools 返回全部 shell 工具（当前为参数化 terminal）。后台任务工具由
// NewTaskRunner 与 TaskRunTool/TaskStatusTool/TaskStopTool 单独提供，
// 由消费方按需选用。
func AllTools(opts ...Option) []kernel.Tool {
	cfg := defaultConfig()
	for _, o := range opts {
		o(cfg)
	}
	return []kernel.Tool{terminalTool(cfg)}
}

func terminalTool(cfg *config) kernel.Tool {
	return tool.MustToolFromFunc(
		func(ctx context.Context, args terminalArgs) (string, error) {
			return terminalWithConfig(ctx, args, cfg)
		},
		tool.WithToolName("terminal"),
		tool.WithToolDescription(fmt.Sprintf("Execute a shell command in the working directory (builds, tests, installs, ops inspection...). Only this tool runs commands: don't invent wrappers. Default timeout %s; long commands must declare the timeout argument (capped at %s); hour-scale tasks use task_run. Output is truncated to 8K chars (head and tail).", cfg.defaultTimeout, cfg.maxTimeout)),
	)
}
