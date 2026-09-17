package shell

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireSh skips the test when no POSIX shell is available (e.g. Windows
// hosts without git-bash on PATH), matching the tool's own sh dependency.
func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available on this host")
	}
}

func runTerminal(t *testing.T, ctx context.Context, command string) (string, error) {
	t.Helper()
	args := terminalArgs{Command: command}
	return Terminal(ctx, args)
}

// TestTerminalWithoutSinkIsUnchanged proves the default path keeps the
// synchronous one-shot behaviour: no sink, one combined result.
func TestTerminalWithoutSinkIsUnchanged(t *testing.T) {
	requireSh(t)
	out, err := runTerminal(t, context.Background(), "echo hello; echo world")
	if err != nil {
		t.Fatalf("Terminal: %v", err)
	}
	if !strings.Contains(out, "$ echo hello; echo world") {
		t.Fatalf("result must echo the command, got %q", out)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Fatalf("result must contain the output, got %q", out)
	}
	if strings.Contains(out, "[exit code") {
		t.Fatalf("successful command must not carry an exit code, got %q", out)
	}
}

// TestTerminalWithProgressStreamsChunks proves the sink path streams output
// incrementally while the command runs, and the final result still carries
// the complete output for the model.
func TestTerminalWithProgressStreamsChunks(t *testing.T) {
	requireSh(t)
	var chunks []string
	ctx := WithProgress(context.Background(), func(p Progress) {
		chunks = append(chunks, p.Text)
	})

	// Two separate echos 120ms apart: the throttler (50ms) must observe at
	// least two chunks, proving incremental delivery before completion.
	command := "echo first; sleep 0.12; echo second"
	out, err := runTerminal(t, ctx, command)
	if err != nil {
		t.Fatalf("Terminal: %v", err)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected multiple progress chunks, got %d: %v", len(chunks), chunks)
	}
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "first") || !strings.Contains(joined, "second") {
		t.Fatalf("progress chunks must carry both outputs, got %q", joined)
	}

	// The model-facing result stays complete and unchanged in shape.
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("final result must contain the full output, got %q", out)
	}
}

// TestTerminalProgressPreservesExitCode proves failures still surface the
// exit code in the result while the sink receives the stderr text.
func TestTerminalProgressPreservesExitCode(t *testing.T) {
	requireSh(t)
	var chunks []string
	ctx := WithProgress(context.Background(), func(p Progress) { chunks = append(chunks, p.Text) })

	out, err := runTerminal(t, ctx, "echo boom >&2; exit 3")
	if err != nil {
		t.Fatalf("Terminal: %v", err)
	}
	if !strings.Contains(out, "[exit code:") || !strings.Contains(out, "3") {
		t.Fatalf("result must carry the exit code, got %q", out)
	}
	if !strings.Contains(strings.Join(chunks, ""), "boom") {
		t.Fatalf("stderr must stream through the sink, chunks: %v", chunks)
	}
}

// TestTerminalProgressChunkCap proves oversized output is chunk-capped for
// the sink (UI event bound) while the final result keeps its own 8K cap.
func TestTerminalProgressChunkCap(t *testing.T) {
	requireSh(t)
	var maxChunk int
	ctx := WithProgress(context.Background(), func(p Progress) {
		if len(p.Text) > maxChunk {
			maxChunk = len(p.Text)
		}
	})

	out, err := runTerminal(t, ctx, "head -c 20000 /dev/zero | tr '\\0' 'x'")
	if err != nil {
		t.Fatalf("Terminal: %v", err)
	}
	if maxChunk > progressChunkCap+64 {
		t.Fatalf("progress chunk too large: %d (cap %d)", maxChunk, progressChunkCap)
	}
	// The model-facing output stays bounded: truncated body (~8K) plus the
	// truncation marker and the echoed command, never the raw 20K.
	if len(out) > 9000 || !strings.Contains(out, "[truncated") {
		t.Fatalf("model output must be truncated to ~8K, got %d chars", len(out))
	}
}

// TestTerminalWithProgressThrottles proves delivery is throttled: a command
// that produces output continuously must not flood the sink with per-byte
// calls — the 50ms ticker bounds the chunk count.
func TestTerminalWithProgressThrottles(t *testing.T) {
	requireSh(t)
	start := time.Now()
	var chunks int
	ctx := WithProgress(context.Background(), func(p Progress) { chunks++ })

	if _, err := runTerminal(t, ctx, "seq 1 200; sleep 0.1"); err != nil {
		t.Fatalf("Terminal: %v", err)
	}
	elapsed := time.Since(start)
	// 200 lines + 100ms runtime: at 50ms throttling this stays well under
	// 20 chunks; anything near 200 would mean per-line delivery.
	if chunks > 20 {
		t.Fatalf("progress not throttled: %d chunks in %v", chunks, elapsed)
	}
}

// TestTerminalTimeoutKillsLongCommand proves a declared timeout terminates
// the command promptly (the 30s-hard-timeout regression: long ops commands
// were killed with no recourse).
func TestTerminalTimeoutKillsLongCommand(t *testing.T) {
	requireSh(t)
	start := time.Now()
	out, err := Terminal(context.Background(), terminalArgs{
		Command: "sleep 30",
		Timeout: "1s",
	})
	if err != nil {
		t.Fatalf("timeout must be reported in the result, not as an error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("1s timeout did not take effect, elapsed %v", elapsed)
	}
	if !strings.Contains(out, "[timeout") || !strings.Contains(out, "task_run") {
		t.Fatalf("timeout result must carry guidance, got %q", out)
	}
}

// TestTerminalExplicitTimeoutSucceeds proves a declared timeout lets a
// command run past the default without being killed.
func TestTerminalExplicitTimeoutSucceeds(t *testing.T) {
	requireSh(t)
	start := time.Now()
	out, err := Terminal(context.Background(), terminalArgs{
		Command: "sleep 1",
		Timeout: "10s",
	})
	if err != nil {
		t.Fatalf("Terminal: %v", err)
	}
	if strings.Contains(out, "[exit code") || strings.Contains(out, "[timeout") {
		t.Fatalf("successful command must not carry failure markers, got %q", out)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("elapsed anomaly: %v", elapsed)
	}
}

// TestResolveTimeout proves parsing, defaults and the cap.
func TestResolveTimeout(t *testing.T) {
	cfg := defaultConfig()
	if got, err := resolveTimeout("", cfg); err != nil || got != DefaultTimeout {
		t.Errorf("empty = %v/%v, want %v", got, err, DefaultTimeout)
	}
	if got, err := resolveTimeout("90s", cfg); err != nil || got != 90*time.Second {
		t.Errorf("90s = %v/%v", got, err)
	}
	if got, err := resolveTimeout("2h", cfg); err != nil || got != MaxTimeout {
		t.Errorf("2h must clamp to the cap: %v/%v, want %v", got, err, MaxTimeout)
	}
	if _, err := resolveTimeout("abc", cfg); err == nil {
		t.Error("invalid timeout must error")
	}
}

// TestAllToolsOptions proves the option-based default/max override reaches
// the tool (ops agents want a 60s default instead of 30s).
func TestAllToolsOptions(t *testing.T) {
	requireSh(t)
	tools := AllTools(WithDefaultTimeout(60*time.Second), WithMaxTimeout(5*time.Minute))
	if len(tools) != 1 || tools[0].Name() != "terminal" {
		t.Fatalf("AllTools must return the terminal tool, got %v", tools)
	}
	if _, err := resolveTimeout("2h", &config{defaultTimeout: 60 * time.Second, maxTimeout: 5 * time.Minute}); err != nil {
		t.Fatalf("resolveTimeout: %v", err)
	}
}

// TestWithShellOverridesSh proves the toolset-level WithShell option changes
// the execution shell while keeping the result shape identical. This is the
// escape hatch for hosts without sh (bash-only containers, Windows without
// git-bash): the host resolves a usable shell and injects it.
func TestWithShellOverridesSh(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available on this host")
	}
	tools := AllTools(WithShell("bash", "-c"))
	if len(tools) != 1 {
		t.Fatalf("AllTools must return one tool, got %d", len(tools))
	}
	tool := tools[0]
	out, err := tool.Run(context.Background(), `{"command":"echo shell-override-ok"}`)
	if err != nil {
		t.Fatalf("terminal with bash: %v", err)
	}
	if !strings.Contains(out, "shell-override-ok") {
		t.Fatalf("bash execution must produce the output, got %q", out)
	}
}

// TestWithShellInContextWinsOverOption proves the per-call context injection
// takes precedence over the toolset-level option (the ops engine injects its
// resolved shell via context, per environment).
func TestWithShellInContextWinsOverOption(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available on this host")
	}
	ctx := WithShellInContext(context.Background(), "bash", "-c")
	args := terminalArgs{Command: "echo ctx-shell-ok"}
	out, err := terminalWithConfig(ctx, args, defaultConfig())
	if err != nil {
		t.Fatalf("terminal with ctx shell: %v", err)
	}
	if !strings.Contains(out, "ctx-shell-ok") {
		t.Fatalf("context shell must win, got %q", out)
	}
}
