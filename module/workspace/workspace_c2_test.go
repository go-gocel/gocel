package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
)

// runGuard executes the guard's tool-call hook for one tool/args pair.
func runGuard(g *Guard, name, args string) error {
	info := &kernel.ToolCallInfo{Name: name, Args: args}
	_, _, err := g.onToolCall(context.Background(), info)
	return err
}

func TestGuard_RenameBlockedOutsideRoot(t *testing.T) {
	root := t.TempDir()
	g := New(root)

	inside := mustArgs(t, map[string]string{
		"old_path": filepath.Join(root, "a.txt"),
		"new_path": filepath.Join(root, "b.txt"),
	})
	if err := runGuard(g, "rename", inside); err != nil {
		t.Fatalf("in-root rename = %v, want nil", err)
	}

	// Renaming a file OUT of the workspace must be blocked (C2: rename was
	// not in the interception table at all).
	outside := mustArgs(t, map[string]string{
		"old_path": filepath.Join(root, "a.txt"),
		"new_path": outsidePath(t),
	})
	if err := runGuard(g, "rename", outside); err == nil {
		t.Fatal("rename out of workspace = nil, want block")
	}
}

func TestGuard_TaskRunCommandChecked(t *testing.T) {
	g := New(t.TempDir())
	if err := runGuard(g, "task_run", `{"command":"go test ./..."}`); err != nil {
		t.Fatalf("safe task_run = %v, want nil", err)
	}
	if err := runGuard(g, "task_run", `{"command":"rm -rf /"}`); err == nil {
		t.Fatal("dangerous task_run = nil, want block (C2: task_run was not intercepted)")
	}
}

func TestGuard_SymlinkCannotEscape(t *testing.T) {
	root := t.TempDir()
	target := outsidePath(t)
	if err := os.WriteFile(target, []byte("s"), 0o600); err != nil {
		t.Skipf("cannot create target: %v", err)
	}
	defer os.Remove(target)
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	g := New(root)
	// The write path is inside the root lexically, but resolves outside —
	// containment must resolve symlinks (C2).
	args := mustArgs(t, map[string]string{"path": link})
	if err := runGuard(g, "write", args); err == nil {
		t.Fatal("write through escaping symlink = nil, want block")
	}
}

// mustArgs marshals a JSON argument string (Windows backslashes must be
// JSON-escaped — hand-built strings would not parse).
func mustArgs(t *testing.T, m map[string]string) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func outsidePath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory for outside-path assertions")
	}
	return filepath.Join(home, "gocel-workspace-outside-check.txt")
}

// TestGuard_ErrorMentionsTool preserves the guard's error contract: blocks
// are hard failures carrying a workspace prefix.
func TestGuard_ErrorMentionsTool(t *testing.T) {
	g := New(t.TempDir())
	err := runGuard(g, "write", `{"path":"`+outsidePath(t)+`"}`)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("err = %v, want workspace-prefixed block", err)
	}
}
