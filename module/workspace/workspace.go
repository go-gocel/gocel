// Package workspace provides a gocel Module that enforces working-directory safety.
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-gocel/gocel/core/host"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/jsonx"
)

// Guard blocks tools from touching anything outside the workspace roots.
// A single-root Guard (New) is the common case; NewMulti aggregates several
// repositories into one workspace — the agent may operate inside any root,
// mirroring multi-root editor workspaces.
//
// Guard 阻止工具触碰工作区根目录之外的任何路径。单根 Guard（New）是常见
// 情况；NewMulti 把多个仓库聚合为一个工作区——Agent 可在任一根内操作，
// 对应多根编辑器工作区。
type Guard struct {
	// Roots lists every allowed root, absolute and cleaned. The first entry
	// is the primary root. Roots is the single source of truth; Root below is
	// kept for compatibility.
	Roots []string
	// Root is the primary root (first of Roots), kept for back-compat.
	Root         string
	AllowOutside bool
	BlockPrefix  []string
	// AllowPrefix lists command prefixes that bypass BlockPrefix checks.
	// A command matching any of these prefixes runs without further blocking,
	// providing an explicit escape hatch for legitimate dangerous-looking commands.
	AllowPrefix []string

	// Policy, when set, upgrades the path guard from prefix containment to
	// the session's permission-tier per-call resolution (DSH
	// sandbox-policy): a write-effect tool whose target is denied by the
	// tier (read-only, or outside the policy's writable roots) is blocked
	// with a PolicyDenial. The blacklist stays as the second line of
	// defense for commands. Nil keeps the legacy roots-only behavior.
	//
	// Policy 非 nil 时，把路径守卫从前缀包含升级为会话权限档位的逐调用
	// 解析（DSH sandbox-policy）：写副作用工具的目标被档位拒绝（只读档，
	// 或位于策略可写根之外）即以 PolicyDenial 阻断。黑名单仍是命令的第
	// 二道防线。nil 保持旧版仅根目录行为。
	Policy kernel.FilePolicy
}

// New creates a single-root guard for the given working directory.
// New 为给定工作目录创建单根守卫。
func New(root string) *Guard {
	return NewMulti(root)
}

// NewMulti creates a guard allowing access inside any of the given roots.
// Roots are made absolute and cleaned; empty entries are dropped. The first
// surviving root is the primary root.
//
// NewMulti 创建允许访问任一给定根目录的守卫。根目录被转为绝对路径并清理；
// 空条目被丢弃；第一个存活的根是主根。
func NewMulti(roots ...string) *Guard {
	cleaned := make([]string, 0, len(roots))
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			abs = filepath.Clean(r)
		}
		if abs == "" {
			continue
		}
		cleaned = append(cleaned, filepath.Clean(abs))
	}
	if len(cleaned) == 0 {
		cleaned = append(cleaned, ".")
	}
	return &Guard{
		Roots: cleaned,
		Root:  cleaned[0],
		BlockPrefix: []string{
			"rm -rf /", "rm -rf ~", "sudo ",
			"chmod 777 /", "mkfs.", "dd if=", "> /dev/",
		},
	}
}

// Register implements kernel.Module: hooks the tool-call guard.
// Register 实现 kernel.Module：挂载工具调用守卫钩子。
func (g *Guard) Register(rt kernel.HookRegistrar) {
	rt.OnToolCall(g.onToolCall)
}

func (g *Guard) onToolCall(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	switch info.Name {
	case "terminal", "task_run":
		// Background shell tasks (task_run) run the same command surface as
		// the terminal and must pass the same blocklist (C2).
		if err := g.checkCommand(info.Args); err != nil {
			g.fireDecision(ctx, info, "hard_blocked", err.Error())
			info.Error = err
			return ctx, info, err
		}
	case "write", "trash":
		if err := g.checkPath(info.Args); err != nil {
			g.fireDecision(ctx, info, "hard_blocked", err.Error())
			info.Error = err
			return ctx, info, err
		}
	case "rename":
		// Renaming can move a file OUT of the workspace; both endpoints
		// must stay inside (C2: rename was not intercepted at all).
		if err := g.checkRenamePaths(info.Args); err != nil {
			g.fireDecision(ctx, info, "hard_blocked", err.Error())
			info.Error = err
			return ctx, info, err
		}
	case "multi_edit":
		if err := g.checkAllPaths(info.Args); err != nil {
			g.fireDecision(ctx, info, "hard_blocked", err.Error())
			info.Error = err
			return ctx, info, err
		}
	}
	return ctx, info, nil
}

// fireDecision 把阻断决策上报给框架决策钩子（audit/observability 订阅）。
// 上报失败只影响留痕，不改变阻断结果。
func (g *Guard) fireDecision(ctx context.Context, info *kernel.ToolCallInfo, decision, reason string) {
	if rt := kernel.RuntimeFromContext(ctx); rt != nil {
		_ = rt.FireDecision(ctx, &kernel.DecisionInfo{
			Tool:     info.Name,
			Args:     info.Args,
			Decision: decision,
			Reason:   reason,
		})
	}
}

func (g *Guard) checkCommand(argsJSON string) error {
	var args struct {
		Command string `json:"command"`
	}
	if err := unmarshalArgs(argsJSON, &args); err != nil {
		return err
	}
	if args.Command == "" {
		return nil
	}
	// Whitelist first: explicitly allowed prefixes bypass the blocklist.
	for _, p := range g.AllowPrefix {
		if strings.HasPrefix(args.Command, p) {
			return nil
		}
	}
	for _, p := range g.BlockPrefix {
		if strings.Contains(args.Command, p) {
			return fmt.Errorf("workspace: BLOCKED dangerous command: %q", p)
		}
	}
	return nil
}

func (g *Guard) checkPath(argsJSON string) error {
	var args struct {
		Path string `json:"path"`
	}
	if err := unmarshalArgs(argsJSON, &args); err != nil {
		return err
	}
	if args.Path == "" {
		return nil
	}
	return g.validatePath(args.Path)
}

// checkRenamePaths validates both endpoints of a rename: the source and the
// destination must each stay inside at least one workspace root.
func (g *Guard) checkRenamePaths(argsJSON string) error {
	var args struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
	}
	if err := unmarshalArgs(argsJSON, &args); err != nil {
		return err
	}
	for _, p := range []string{args.OldPath, args.NewPath} {
		if p == "" {
			continue
		}
		if err := g.validatePath(p); err != nil {
			return err
		}
	}
	return nil
}

// checkAllPaths validates every path in multi_edit's edits[].path array.
func (g *Guard) checkAllPaths(argsJSON string) error {
	var args struct {
		Edits []struct {
			Path string `json:"path"`
		} `json:"edits"`
	}
	if err := unmarshalArgs(argsJSON, &args); err != nil {
		return err
	}
	for _, e := range args.Edits {
		if err := g.validatePath(e.Path); err != nil {
			return err
		}
	}
	return nil
}

// unmarshalArgs decodes tool argument JSON. Empty input is tolerated and
// treated as "no arguments". Malformed JSON is rejected: a safety guard
// must not silently pass through unparseable input.
func unmarshalArgs(argsJSON string, v any) error {
	if strings.TrimSpace(argsJSON) == "" {
		return nil
	}
	if err := jsonx.Unmarshal([]byte(argsJSON), v); err != nil {
		return fmt.Errorf("workspace: invalid tool args: %w", err)
	}
	return nil
}

func (g *Guard) validatePath(path string) error {
	// Tier-aware per-call resolution (DSH sandbox-policy): when a FilePolicy
	// is mounted, the write tier itself decides whether the path is writable.
	// read-only denies every write; workspace-write confines to the policy's
	// writable roots (symlink-resolved); danger-full-access allows.
	if g.Policy != nil {
		if err := g.Policy.Check(types.FileOpWrite, path); err != nil {
			return err
		}
	}
	if g.AllowOutside {
		return nil
	}
	// The tools resolve relative paths against the process cwd, so that
	// interpretation as well as the interpretation against every workspace
	// root must stay inside at least one root.
	interps := []string{path}
	if !filepath.IsAbs(path) {
		for _, root := range g.Roots {
			interps = append(interps, filepath.Join(root, path))
		}
	}
	for _, p := range interps {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = filepath.Clean(p)
		}
		if !g.insideAny(abs) {
			return fmt.Errorf("workspace: BLOCKED path outside workspace: %s", path)
		}
	}
	return nil
}

// insideAny reports whether abs is inside at least one workspace root,
// resolving symlinks so a link cannot smuggle a path outside (C2).
func (g *Guard) insideAny(abs string) bool {
	for _, root := range g.Roots {
		if host.WithinRoot(root, abs) {
			return true
		}
	}
	return false
}
