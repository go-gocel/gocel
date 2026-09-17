package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/host"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"
	"github.com/go-gocel/gocel/core/types"
)

// guard mirrors real usage: the workspace root is the process working
// directory (as gocode does with workspace.New(workDir)).
func guard(t *testing.T) *Guard {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return New(wd)
}

func jsonArgs(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestCheckCommand_BlocksDangerousPrefix(t *testing.T) {
	g := guard(t)
	if err := g.checkCommand(jsonArgs(map[string]string{"command": "rm -rf /etc"})); err == nil {
		t.Fatal("dangerous command should be blocked")
	}
	if err := g.checkCommand(jsonArgs(map[string]string{"command": "go build ./..."})); err != nil {
		t.Fatalf("safe command should pass: %v", err)
	}
}

func TestCheckCommand_AllowPrefixBypassesBlocklist(t *testing.T) {
	g := guard(t)
	g.AllowPrefix = []string{"sudo -k "}

	// sudo -k <command> is an explicit allowlist match and bypasses the blocklist.
	if err := g.checkCommand(jsonArgs(map[string]string{"command": "sudo -k rm -rf /tmp/x"})); err != nil {
		t.Fatalf("allowlisted prefix should bypass blocklist: %v", err)
	}
	// Without the prefix the same payload is still blocked.
	if err := g.checkCommand(jsonArgs(map[string]string{"command": "rm -rf /tmp/x"})); err == nil {
		t.Fatal("blocked prefix must still be blocked without allowlist")
	}
}

func TestCheckPathInside(t *testing.T) {
	g := guard(t)
	for _, p := range []string{
		"a.go",
		"sub/dir/b.go",
	} {
		if err := g.checkPath(jsonArgs(map[string]string{"path": p})); err != nil {
			t.Errorf("path %q should be allowed, got: %v", p, err)
		}
	}
}

func TestCheckPathOutside(t *testing.T) {
	g := guard(t)
	wd, _ := os.Getwd()
	root := filepath.Clean(wd)
	for _, p := range []string{
		filepath.Join(root, "..", "escape.go"),
		"../outside.go",
		filepath.Join(root+"-sibling", "a.go"),
		filepath.Join(string(filepath.Separator), "etc", "passwd"),
	} {
		if err := g.checkPath(jsonArgs(map[string]string{"path": p})); err == nil {
			t.Errorf("path %q should be blocked", p)
		}
	}
}

// TestMultiRootGuard proves a multi-root guard allows paths inside ANY root
// and still blocks anything outside all of them.
func TestMultiRootGuard(t *testing.T) {
	wd, _ := os.Getwd()
	root := filepath.Clean(wd)
	sibling := filepath.Join(filepath.Dir(root), "workspace-sibling-fixture")
	g := NewMulti(root, sibling)

	allowed := []string{
		"a.go",
		filepath.Join(root, "x.go"),
		filepath.Join(sibling, "y.go"),
		// Relative interpretation against the second root must be allowed.
		filepath.Join(sibling, "sub", "z.go"),
	}
	for _, p := range allowed {
		if err := g.checkPath(jsonArgs(map[string]string{"path": p})); err != nil {
			t.Errorf("path %q should be allowed in multi-root workspace: %v", p, err)
		}
	}

	blocked := []string{
		filepath.Join(root, "..", "escape.go"),
		"../outside.go",
		filepath.Join(string(filepath.Separator), "etc", "passwd"),
	}
	for _, p := range blocked {
		if err := g.checkPath(jsonArgs(map[string]string{"path": p})); err == nil {
			t.Errorf("path %q should be blocked in multi-root workspace", p)
		}
	}
}

func TestNewMultiPrimaryRoot(t *testing.T) {
	wd, _ := os.Getwd()
	root := filepath.Clean(wd)
	g := NewMulti(root, "/nonexistent-other-root")
	if len(g.Roots) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(g.Roots))
	}
	if g.Root != root {
		t.Fatalf("primary root should be the first argument, got %q", g.Root)
	}
	// New keeps single-root semantics.
	single := New(root)
	if len(single.Roots) != 1 || single.Roots[0] != root {
		t.Fatalf("New should create a single-root guard, got %v", single.Roots)
	}
}

func TestCheckAllPathsMultiEdit(t *testing.T) {
	g := guard(t)
	type edit struct {
		Path   string `json:"path"`
		OldStr string `json:"old_str"`
		NewStr string `json:"new_str"`
	}
	escaped := jsonArgs(map[string]any{"edits": []edit{
		{Path: "a.go", OldStr: "x", NewStr: "y"},
		{Path: "../escape.go", OldStr: "x", NewStr: "y"},
	}})
	if err := g.checkAllPaths(escaped); err == nil {
		t.Fatal("multi_edit with an escaping edit should be blocked")
	}
	ok := jsonArgs(map[string]any{"edits": []edit{
		{Path: "a.go", OldStr: "x", NewStr: "y"},
		{Path: "sub/b.go", OldStr: "x", NewStr: "y"},
	}})
	if err := g.checkAllPaths(ok); err != nil {
		t.Fatalf("multi_edit inside workspace should be allowed, got: %v", err)
	}
}

func TestOnBeforeToolCallNames(t *testing.T) {
	g := guard(t)
	ctx := context.Background()

	cases := []struct {
		name string
		args string
		ok   bool
	}{
		{"write", jsonArgs(map[string]string{"path": "a.go", "content": "x"}), true},
		{"write", jsonArgs(map[string]string{"path": "../escape.go", "content": "x"}), false},
		{"multi_edit", jsonArgs(map[string]any{"edits": []map[string]string{{"path": "a.go", "old_str": "x", "new_str": "y"}}}), true},
		{"multi_edit", jsonArgs(map[string]any{"edits": []map[string]string{{"path": "/etc/passwd", "old_str": "x", "new_str": "y"}}}), false},
		{"trash", jsonArgs(map[string]string{"path": "a.go"}), true},
		{"trash", jsonArgs(map[string]string{"path": "/etc/passwd"}), false},
		{"terminal", jsonArgs(map[string]string{"command": "ls"}), true},
		{"terminal", jsonArgs(map[string]string{"command": "sudo rm -rf /"}), false},
	}
	for _, c := range cases {
		info := &kernel.ToolCallInfo{Name: c.name, Args: c.args}
		_, _, err := g.onToolCall(ctx, info)
		if (err == nil) != c.ok {
			t.Errorf("%s %s: blocked=%v, want %v (err=%v)", c.name, c.args, err != nil, !c.ok, err)
		}
	}
}

// TestOnToolCall_FiresDecisionOnBlock 验证阻断时通过框架决策钩子上报。
func TestOnToolCall_FiresDecisionOnBlock(t *testing.T) {
	g := guard(t)
	rt := runtime.NewRuntime(nil, nil)
	var got *kernel.DecisionInfo
	rt.OnDecision(func(ctx context.Context, info *kernel.DecisionInfo) (context.Context, *kernel.DecisionInfo, error) {
		got = info
		return ctx, info, nil
	})
	ctx := kernel.WithRuntime(context.Background(), rt)

	_, info, err := g.onToolCall(ctx, &kernel.ToolCallInfo{
		Name: "terminal",
		Args: jsonArgs(map[string]string{"command": "sudo rm -rf /"}),
	})
	if err == nil {
		t.Fatal("dangerous command should be blocked")
	}
	if info == nil || info.Error == nil {
		t.Fatal("info.Error should be set on block")
	}
	if got == nil {
		t.Fatal("decision hook not fired")
	}
	if got.Tool != "terminal" || got.Decision != "hard_blocked" {
		t.Fatalf("decision = %+v, want terminal/hard_blocked", got)
	}
	if !strings.Contains(got.Reason, "BLOCKED") {
		t.Fatalf("reason = %q, want BLOCKED detail", got.Reason)
	}
}

// TestOnToolCall_NoDecisionOnPass 验证放行时不产生决策事件。
func TestOnToolCall_NoDecisionOnPass(t *testing.T) {
	g := guard(t)
	rt := runtime.NewRuntime(nil, nil)
	fired := false
	rt.OnDecision(func(ctx context.Context, info *kernel.DecisionInfo) (context.Context, *kernel.DecisionInfo, error) {
		fired = true
		return ctx, info, nil
	})
	ctx := kernel.WithRuntime(context.Background(), rt)

	if _, _, err := g.onToolCall(ctx, &kernel.ToolCallInfo{
		Name: "terminal",
		Args: jsonArgs(map[string]string{"command": "go build ./..."}),
	}); err != nil {
		t.Fatalf("allowed command should pass: %v", err)
	}
	if fired {
		t.Fatal("no decision expected for an allowed command")
	}
}

// TestPolicy_ReadOnlyDeniesWrites: with a read-only FilePolicy mounted, a
// write-effect tool is denied per-call even when the path sits inside the
// workspace roots — the tier wins over prefix containment (DSH
// sandbox-policy).
func TestPolicy_ReadOnlyDeniesWrites(t *testing.T) {
	dir := t.TempDir()
	policy := host.NewDefaultFilePolicy(dir)
	policy.SetMode(types.PermissionReadOnly)
	g := NewMulti(dir)
	g.Policy = policy

	err := g.checkPath(jsonArgs(map[string]string{"path": filepath.Join(dir, "in.txt")}))
	if err == nil {
		t.Fatal("read-only tier must deny a write inside the workspace")
	}
	if !kernel.IsPolicyDenial(err) {
		t.Fatalf("denial = %v, want PolicyDenial", err)
	}
}

// TestPolicy_WorkspaceWriteConfines: with a workspace-write policy, writes
// inside the writable roots pass and outside are denied — per-call
// resolution replaces pure prefix containment.
func TestPolicy_WorkspaceWriteConfines(t *testing.T) {
	dir := t.TempDir()
	policy := host.NewDefaultFilePolicy(dir) // workspace-write default
	g := NewMulti(dir)
	g.Policy = policy

	if err := g.checkPath(jsonArgs(map[string]string{"path": filepath.Join(dir, "in.txt")})); err != nil {
		t.Fatalf("in-root write under workspace-write = %v, want nil", err)
	}
	if err := g.checkPath(jsonArgs(map[string]string{"path": filepath.Join(dir, "..", "escape.txt")})); err == nil {
		t.Fatal("out-of-root write under workspace-write must be denied")
	}
}

// TestPolicy_NilKeepsLegacyBehavior: without a policy the guard keeps the
// legacy roots-only containment.
func TestPolicy_NilKeepsLegacyBehavior(t *testing.T) {
	dir := t.TempDir()
	g := NewMulti(dir)
	if err := g.checkPath(jsonArgs(map[string]string{"path": filepath.Join(dir, "in.txt")})); err != nil {
		t.Fatalf("legacy guard must allow in-root writes, got %v", err)
	}
}
