package rename

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/internal/toolutil"
)

// Provider is the tool provider that exposes the rename tool.
// Provider 是提供 rename 工具的工具提供者。
type Provider struct{}

// New creates a new rename provider.
// New 创建新的 rename 工具提供者。
func New() *Provider { return &Provider{} }

// ListTools returns the tools provided by this provider.
// ListTools 返回该提供者提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{&renameTool{}}
}

type renameTool struct{}

// Name returns the tool name "rename".
// Name 返回工具名 "rename"。
func (t *renameTool) Name() string { return "rename" }
// Description returns the tool description.
// Description 返回工具描述。
func (t *renameTool) Description() string {
	return "Rename or move files. Single mode: old_path→new_path. Batch mode: pattern+old_str+new_str renames all matched files. Supports ** glob, dry_run, force overwrite."
}
// Schema returns the JSON schema of the tool arguments.
// Schema 返回工具参数的 JSON schema。
func (t *renameTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"old_path": map[string]any{"type": "string", "description": "Current file path (for single rename)"},
			"new_path": map[string]any{"type": "string", "description": "New file path (for single rename)"},
			"pattern":  map[string]any{"type": "string", "description": "Glob pattern to find files, supports ** recursion (for batch rename)"},
			"old_str":  map[string]any{"type": "string", "description": "String in filename to replace (for batch rename)"},
			"new_str":  map[string]any{"type": "string", "description": "Replacement string (for batch rename)"},
			"dry_run":  map[string]any{"type": "boolean", "description": "Preview rename without executing"},
			"force":    map[string]any{"type": "boolean", "description": "Overwrite destination if it already exists"},
		},
	}
}

// ToolMeta returns the metadata of the tool.
// ToolMeta 返回工具元数据。
func (t *renameTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{Source: "builtin"}
}

type renameArgs struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Pattern string `json:"pattern"`
	OldStr  string `json:"old_str"`
	NewStr  string `json:"new_str"`
	DryRun  bool   `json:"dry_run"`
	Force   bool   `json:"force"`
}

type renameOp struct{ old, new string }

// Run performs the rename described by the given JSON args and returns the results.
// Run 根据给定的 JSON 参数执行重命名并返回结果。
func (t *renameTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args renameArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("rename: invalid args: %w", err)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	// Single rename
	if args.OldPath != "" && args.NewPath != "" {
		return t.singleRename(ctx, &args)
	}

	// Batch rename
	if args.Pattern != "" && args.OldStr != "" {
		return t.batchRename(ctx, &args)
	}

	return "", fmt.Errorf("rename: provide old_path+new_path (single) or pattern+old_str+new_str (batch)")
}

// ---------------------------------------------------------------------------
// Single rename
// ---------------------------------------------------------------------------

func (t *renameTool) singleRename(ctx context.Context, args *renameArgs) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	if args.DryRun {
		return toolutil.FormatResult(fmt.Sprintf("would rename %s → %s", args.OldPath, args.NewPath), map[string]any{
			"from": args.OldPath, "to": args.NewPath, "dry_run": true,
		}), nil
	}

	// Check if old path exists
	if _, err := os.Stat(args.OldPath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("rename: source not found: %s", args.OldPath)
		}
		return "", fmt.Errorf("rename: stat: %w", err)
	}

	// Bug 3 fix: distinguish existing file vs existing directory
	if !args.Force {
		fi, err := os.Stat(args.NewPath)
		if err == nil {
			if fi.IsDir() {
				// Moving into an existing directory — valid, just do it
			} else {
				return "", fmt.Errorf("rename: destination already exists: %s (use force=true to overwrite)", args.NewPath)
			}
		}
	}

	dir := filepath.Dir(args.NewPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("rename: mkdir: %w", err)
	}
	if err := os.Rename(args.OldPath, args.NewPath); err != nil {
		return "", fmt.Errorf("rename: %w", err)
	}
	return toolutil.FormatResult(fmt.Sprintf("renamed %s → %s", args.OldPath, args.NewPath), map[string]any{
		"from": args.OldPath, "to": args.NewPath,
	}), nil
}

// ---------------------------------------------------------------------------
// Batch rename
// ---------------------------------------------------------------------------

func (t *renameTool) batchRename(ctx context.Context, args *renameArgs) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	matches, err := globFiles(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("rename: pattern: %w", err)
	}
	if len(matches) == 0 {
		return toolutil.FormatResult("no files matched the pattern", map[string]any{
			"renamed": []any{}, "total": 0,
		}), nil
	}

	ops := make([]renameOp, 0)
	for _, m := range matches {
		base := filepath.Base(m)
		if !strings.Contains(base, args.OldStr) {
			continue
		}
		newBase := strings.ReplaceAll(base, args.OldStr, args.NewStr)
		newPath := filepath.Join(filepath.Dir(m), newBase)
		ops = append(ops, renameOp{m, newPath})
	}

	if len(ops) == 0 {
		return toolutil.FormatResult(fmt.Sprintf("no files matched old_str %q in pattern results", args.OldStr), map[string]any{
			"renamed": []any{}, "total": 0,
		}), nil
	}

	if args.DryRun {
		renamedList := make([]map[string]string, len(ops))
		for i, op := range ops {
			renamedList[i] = map[string]string{"from": op.old, "to": op.new}
		}
		return toolutil.FormatResult(fmt.Sprintf("would rename %d files", len(ops)), map[string]any{
			"renamed": renamedList, "total": len(ops), "dry_run": true,
		}), nil
	}

	// Bug 1 fix: Phase 1 — validate all destinations before any rename
	for _, op := range ops {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !args.Force {
			fi, err := os.Stat(op.new)
			if err == nil && !fi.IsDir() {
				return "", fmt.Errorf("rename: '%s' → '%s': destination already exists (use force=true)", op.old, op.new)
			}
		}
	}

	// Bug 1 fix: Phase 2 — execute with rollback on failure
	renamedList := make([]map[string]string, 0, len(ops))
	completed := 0
	for _, op := range ops {
		if ctx.Err() != nil {
			// Rollback
			t.rollback(ops[:completed])
			return "", ctx.Err()
		}

		dir := filepath.Dir(op.new)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.rollback(ops[:completed])
			return "", fmt.Errorf("rename: mkdir: %w (rolled back %d files)", err, completed)
		}
		if err := os.Rename(op.old, op.new); err != nil {
			t.rollback(ops[:completed])
			return "", fmt.Errorf("rename: %s: %w (rolled back %d files)", op.old, err, completed)
		}
		completed++
		renamedList = append(renamedList, map[string]string{"from": op.old, "to": op.new})
	}

	return toolutil.FormatResult(fmt.Sprintf("renamed %d files", len(ops)), map[string]any{
		"renamed": renamedList, "total": len(ops),
	}), nil
}

// rollback reverts a list of completed rename operations.
func (t *renameTool) rollback(ops []renameOp) {
	for i := len(ops) - 1; i >= 0; i-- {
		os.Rename(ops[i].new, ops[i].old) // best-effort
	}
}

// ---------------------------------------------------------------------------
// Glob with ** support
// ---------------------------------------------------------------------------

func globFiles(pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(pattern)
	}

	// For ** patterns, walk from the directory before the first **
	var matches []string

	// Determine base directory
	idx := strings.Index(pattern, "**")
	prefix := strings.TrimSuffix(pattern[:idx], "/")
	prefix = strings.TrimSuffix(prefix, string(filepath.Separator))
	base := "."
	if prefix != "" {
		base = filepath.Dir(prefix)
	}

	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if toolutil.GlobMatch(pattern, path) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// AllTools returns all tools in this package.
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
