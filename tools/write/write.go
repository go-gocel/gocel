package write

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/internal/toolutil"
)

// Provider supplies the file-writing tool.
//
// Provider 提供文件写入工具。
type Provider struct{}

// New creates a write provider.
//
// New 创建写文件 provider。
func New() *Provider { return &Provider{} }

// ListTools returns the provider's tools.
//
// ListTools 返回该 provider 提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{&writeFileTool{}}
}

type writeFileTool struct{}

// Name returns the tool name "write".
//
// Name 返回工具名 "write"。
func (t *writeFileTool) Name() string { return "write" }
// Description describes the write tool.
//
// Description 描述 write 工具：把内容写入文件，必要时自动创建父目录。
func (t *writeFileTool) Description() string {
	return "Write content to a file. Creates parent directories if needed."
}
// Schema returns the JSON schema for the tool arguments.
//
// Schema 返回工具参数的 JSON schema。
func (t *writeFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":          map[string]any{"type": "string", "description": "Path to the file"},
			"content":       map[string]any{"type": "string", "description": "Content to write"},
			"create_backup": map[string]any{"type": "boolean", "description": "Create .bak backup before overwriting (default false)"},
			"expect_version": map[string]any{
				"type":        "string",
				"description": "Version fingerprint from a prior read of this path. When set, the write is rejected if the file changed since that read (stale version) — prevents blind overwrites of externally modified files.",
			},
			"create_if_absent": map[string]any{
				"type":        "boolean",
				"description": "Only create the file when it does not exist; reject when it already does (default false).",
			},
		},
		"required": []string{"path", "content"},
	}
}
// ToolMeta returns the tool metadata.
//
// ToolMeta 返回工具元数据。
func (t *writeFileTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }
// Run writes the content to the file, honoring the version guard, backup,
// and create-if-absent options.
//
// Run 把内容写入文件，并遵循版本校验、备份与 create_if_absent 选项。
func (t *writeFileTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Path           string `json:"path"`
		Content        string `json:"content"`
		CreateBackup   bool   `json:"create_backup"`
		ExpectVersion  string `json:"expect_version"`
		CreateIfAbsent bool   `json:"create_if_absent"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		err = fmt.Errorf("write_file: invalid args: %w", err)
		return toolutil.FormatError(err), err
	}
	select {
	case <-ctx.Done():
		return toolutil.FormatError(ctx.Err()), ctx.Err()
	default:
	}

	// Version guard (DSH fs-observation-policy semantics): a stale-version
	// expectation rejects the write before any mutation — the file changed
	// since the caller's read.
	if args.ExpectVersion != "" {
		if err := toolutil.CheckVersion(args.Path, args.ExpectVersion); err != nil {
			return toolutil.FormatError(err), err
		}
	}
	// createIfAbsent guard: the write must not clobber an existing file.
	if args.CreateIfAbsent {
		if _, err := os.Stat(args.Path); err == nil {
			err := fmt.Errorf("write: %q already exists and create_if_absent is set", args.Path)
			return toolutil.FormatError(err), err
		}
	}

	// Backup existing file if requested
	backupPath := ""
	if args.CreateBackup {
		if _, err := os.Stat(args.Path); err == nil {
			backupPath = args.Path + ".bak"
			if err := copyFile(args.Path, backupPath); err != nil {
				err = fmt.Errorf("write: create backup: %w", err)
				return toolutil.FormatError(err), err
			}
		}
	}

	dir := filepath.Dir(args.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		err = fmt.Errorf("write_file: create dir: %w", err)
		return toolutil.FormatError(err), err
	}
	if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
		err = fmt.Errorf("write_file: %w", err)
		return toolutil.FormatError(err), err
	}
	data := map[string]any{
		"path": args.Path,
		"size": len(args.Content),
	}
	if backupPath != "" {
		data["backup"] = backupPath
	}
	if v, err := toolutil.FileVersion(args.Path); err == nil {
		data["version"] = v
	}
	return toolutil.FormatResult(fmt.Sprintf("written to %s (%d bytes)", args.Path, len(args.Content)), data), nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// AllTools returns all tools in this package.
//
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
