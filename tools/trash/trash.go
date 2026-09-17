package trash

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-gocel/gocel/internal/toolutil"

	"github.com/go-gocel/gocel/core/kernel"
)

// Provider moves files to the project's .gocode/.trash/ directory.
//
// Provider 把文件移入项目的 .gocode/.trash/ 目录。
type Provider struct{}

// New creates a trash provider.
//
// New 创建回收站 provider。
func New() *Provider { return &Provider{} }

// ListTools returns the provider's tools.
//
// ListTools 返回该 provider 提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{
		&trashTool{},
		&restoreTrashTool{},
		&trashListTool{},
	}
}

type trashListTool struct{}

// Name returns the tool name "trash_list".
//
// Name 返回工具名 "trash_list"。
func (t *trashListTool) Name() string { return "trash_list" }
// Description describes the trash_list tool.
//
// Description 描述 trash_list 工具：列出回收站中的文件及其原始位置、日期和大小。
func (t *trashListTool) Description() string {
	return "List files in trash with original location, date, and size."
}
// Schema returns the JSON schema for the tool arguments.
//
// Schema 返回工具参数的 JSON schema。
func (t *trashListTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}
// ToolMeta returns the tool metadata.
//
// ToolMeta 返回工具元数据。
func (t *trashListTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }
// Run lists the files currently in the trash directory.
//
// Run 列出回收站目录中当前的文件。
func (t *trashListTool) Run(ctx context.Context, argsJSON string) (string, error) {
	trashDir := projectTrashDir()
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		if os.IsNotExist(err) {
			return toolutil.FormatResult("trash is empty", map[string]any{"items": []any{}, "total": 0}), nil
		}
		return "", fmt.Errorf("trash_list: read trash dir: %w", err)
	}

	if len(entries) == 0 {
		return toolutil.FormatResult("trash is empty", map[string]any{"items": []any{}, "total": 0}), nil
	}

	type item struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		ModTime string `json:"mod_time"`
	}
	var items []item

	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	return toolutil.FormatResult(fmt.Sprintf("trash contains %d items", len(items)), map[string]any{
		"items": items,
		"total": len(items),
	}), nil
}

type trashTool struct{}

// Name returns the tool name "trash".
//
// Name 返回工具名 "trash"。
func (t *trashTool) Name() string { return "trash" }
// Description describes the trash tool.
//
// Description 描述 trash 工具：把文件移入回收站（可恢复）。
func (t *trashTool) Description() string {
	return "Move files to trash (recoverable). Files are moved to .gocode/.trash/ with a timestamp. Use restore_trash to recover."
}
// Schema returns the JSON schema for the tool arguments.
//
// Schema 返回工具参数的 JSON schema。
func (t *trashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "File or directory path to move to trash"},
		},
		"required": []string{"path"},
	}
}
// ToolMeta returns the tool metadata.
//
// ToolMeta 返回工具元数据。
func (t *trashTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }
// Run moves the file or directory at args.Path into the trash directory.
//
// Run 把 args.Path 指向的文件或目录移入回收站目录。
func (t *trashTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("trash: invalid args: %w", err)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	if args.Path == "" {
		return "", fmt.Errorf("trash: path is required")
	}

	info, err := os.Stat(args.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("trash: %q does not exist", args.Path)
		}
		return "", fmt.Errorf("trash: stat: %w", err)
	}

	trashDir := projectTrashDir()
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		return "", fmt.Errorf("trash: create trash dir: %w", err)
	}

	baseName := filepath.Base(args.Path)
	timestamp := time.Now().Format("20060102_150405")
	trashName := fmt.Sprintf("%s_%s", baseName, timestamp)
	if info.IsDir() {
		trashName += ".trash"
	}
	destPath := filepath.Join(trashDir, trashName)
	// Same-basename files trashed within one second collide on the
	// second-resolution name; a rename would silently overwrite the
	// earlier victim (data loss). Uniquify when the target exists.
	if _, err := os.Stat(destPath); err == nil {
		destPath = filepath.Join(trashDir, fmt.Sprintf("%s_%d", trashName, time.Now().UnixNano()))
	}

	// Record original path in manifest
	manifestPath := filepath.Join(trashDir, ".manifest.jsonl")
	absPath, _ := filepath.Abs(args.Path)
	manifestEntry := map[string]string{
		"original":  absPath,
		"trashed":   destPath,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	data, _ := json.Marshal(manifestEntry)
	f, errO := os.OpenFile(manifestPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if errO == nil {
		f.Write(data)
		f.Write([]byte("\n"))
		f.Close()
	}

	if err := os.Rename(args.Path, destPath); err != nil {
		return "", fmt.Errorf("trash: move to trash: %w", err)
	}

	return toolutil.FormatResult(fmt.Sprintf("trashed %s → %s", args.Path, destPath), map[string]any{
		"original": args.Path,
		"trashed":  destPath,
	}), nil
}

type restoreTrashTool struct{}

// Name returns the tool name "restore_trash".
//
// Name 返回工具名 "restore_trash"。
func (t *restoreTrashTool) Name() string { return "restore_trash" }
// Description describes the restore_trash tool.
//
// Description 描述 restore_trash 工具：把文件从回收站恢复到原始位置。
func (t *restoreTrashTool) Description() string {
	return "Restore a file from trash back to its original location"
}
// Schema returns the JSON schema for the tool arguments.
//
// Schema 返回工具参数的 JSON schema。
func (t *restoreTrashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Trash file path to restore (e.g., .gocode/.trash/filename_20240101_120000)"},
		},
		"required": []string{"path"},
	}
}
// ToolMeta returns the tool metadata.
//
// ToolMeta 返回工具元数据。
func (t *restoreTrashTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }
// Run restores a trashed file back to its original location.
//
// Run 把已回收的文件恢复到其原始位置。
func (t *restoreTrashTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("restore_trash: invalid args: %w", err)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	if args.Path == "" {
		return "", fmt.Errorf("restore_trash: path is required")
	}

	if _, err := os.Stat(args.Path); os.IsNotExist(err) {
		return "", fmt.Errorf("restore_trash: %q not found in trash", args.Path)
	}

	trashName := filepath.Base(args.Path)
	baseName := strings.TrimSuffix(trashName, ".trash")
	// Strip trailing _yyyymmdd_hhmmss suffix (15 chars + 1 underscore = 16 chars)
	if len(baseName) > 16 && baseName[len(baseName)-16] == '_' {
		baseName = baseName[:len(baseName)-16]
	}

	cwd, _ := os.Getwd()
	// Look up original path from manifest
	trashDir := projectTrashDir()
	destPath := filepath.Join(cwd, baseName) // default fallback
	manifestPath := filepath.Join(trashDir, ".manifest.jsonl")
	if data, err := os.ReadFile(manifestPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			var entry struct {
				Original string `json:"original"`
				Trashed  string `json:"trashed"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && strings.HasSuffix(args.Path, entry.Trashed) {
				destPath = entry.Original
				break
			}
		}
	}

	if _, err := os.Stat(destPath); err == nil {
		return "", fmt.Errorf("restore_trash: %q already exists at destination; remove it first or rename", destPath)
	}

	if err := os.Rename(args.Path, destPath); err != nil {
		return "", fmt.Errorf("restore_trash: restore: %w", err)
	}

	return toolutil.FormatResult(fmt.Sprintf("restored %s → %s", args.Path, destPath), map[string]any{
		"trashed":  args.Path,
		"restored": destPath,
	}), nil
}

func projectTrashDir() string {
	dir := filepath.Join(".gocode", ".trash")
	return dir
}

// AllTools returns all tools in this package.
//
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
