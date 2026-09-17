package multiedit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/internal/toolutil"
)

// Provider is the tool provider that exposes the multi_edit tool.
// Provider 是提供 multi_edit 工具的工具提供者。
type Provider struct{}

// New creates a new multi_edit provider.
// New 创建新的 multi_edit 工具提供者。
func New() *Provider { return &Provider{} }

// ListTools returns the tools provided by this provider.
// ListTools 返回该提供者提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{&multiEditTool{}}
}

type editOp struct {
	Path       string `json:"path"`
	OldStr     string `json:"old_str"`
	NewStr     string `json:"new_str"`
	ReplaceAll bool   `json:"replace_all"`
	// ExpectVersion, when set, requires the file to still carry the
	// version fingerprint from a prior read (DSH fs-observation-policy):
	// a stale file rejects the edit instead of being modified blind.
	ExpectVersion string `json:"expect_version,omitempty"`
}

type fileResult struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	Preview string `json:"preview,omitempty"`
}

type fileEdit struct {
	old, new   string
	replaceAll bool
	expectVer  string
}

type multiEditTool struct{}

// Name returns the tool name "multi_edit".
// Name 返回工具名 "multi_edit"。
func (t *multiEditTool) Name() string { return "multi_edit" }
// Description returns the tool description.
// Description 返回工具描述。
func (t *multiEditTool) Description() string {
	return "Edit one or more files: 'edits' array (path/old_str/new_str) for per-file edits, or 'pattern'+'old_str'+'new_str' for cross-file replacement. Supports regex, replace_all, preview, dry_run."
}
// Schema returns the JSON schema of the tool arguments.
// Schema 返回工具参数的 JSON schema。
func (t *multiEditTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edits": map[string]any{
				"type":        "array",
				"description": "Array of per-file edits (each: path, old_str, new_str; optional replace_all)",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":        map[string]any{"type": "string", "description": "File path"},
						"old_str":     map[string]any{"type": "string", "description": "Text to find"},
						"new_str":     map[string]any{"type": "string", "description": "Replacement text"},
						"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences in this file (default false)"},
					},
					"required": []string{"path", "old_str", "new_str"},
				},
			},
			"pattern":     map[string]any{"type": "string", "description": "Glob of files to edit (cross-file mode, e.g. \"src/**/*.ts\")"},
			"old_str":     map[string]any{"type": "string", "description": "Text/regex to find (cross-file mode)"},
			"new_str":     map[string]any{"type": "string", "description": "Replacement text (cross-file mode)"},
			"regex":       map[string]any{"type": "boolean", "description": "Regex old_str/new_str; $1/$2 captures in new_str (default false)"},
			"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences (default false)"},
			"preview":     map[string]any{"type": "boolean", "description": "Show unified diff without writing (default false)"},
			"dry_run":     map[string]any{"type": "boolean", "description": "List affected files without writing (default false)"},
			"include":     map[string]any{"type": "string", "description": "Only files matching this glob (cross-file mode)"},
			"exclude":     map[string]any{"type": "string", "description": "Skip files matching this glob (cross-file mode)"},
		},
	}
}

// ToolMeta returns the metadata of the tool.
// ToolMeta 返回工具元数据。
func (t *multiEditTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{Source: "builtin"}
}

type multiEditArgs struct {
	Edits      []editOp `json:"edits"`
	Pattern    string   `json:"pattern"`
	OldStr     string   `json:"old_str"`
	NewStr     string   `json:"new_str"`
	Regex      bool     `json:"regex"`
	ReplaceAll bool     `json:"replace_all"`
	DryRun     bool     `json:"dry_run"`
	Preview    bool     `json:"preview"`
	Include    string   `json:"include"`
	Exclude    string   `json:"exclude"`
}

// Run applies the edits described by the given JSON args and returns the results.
// Run 根据给定的 JSON 参数执行编辑并返回结果。
func (t *multiEditTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args multiEditArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("multi_edit: invalid args: %w", err)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	if args.Pattern != "" {
		return t.runCrossFile(ctx, &args)
	}
	if len(args.Edits) == 0 {
		return "", fmt.Errorf("multi_edit: provide either 'edits' array or 'pattern' + 'old_str' + 'new_str'")
	}
	return t.runPerFile(ctx, &args)
}

// ---------------------------------------------------------------------------
// Mode A — per-file unique edits (also covers single-file case)
// ---------------------------------------------------------------------------

func (t *multiEditTool) runPerFile(ctx context.Context, args *multiEditArgs) (string, error) {
	fileEdits := make(map[string][]fileEdit)
	order := make([]string, 0)

	for _, edit := range args.Edits {
		if _, ok := fileEdits[edit.Path]; !ok {
			fileEdits[edit.Path] = nil
			order = append(order, edit.Path)
		}
		fileEdits[edit.Path] = append(fileEdits[edit.Path], fileEdit{
			old:        edit.OldStr,
			new:        edit.NewStr,
			replaceAll: edit.ReplaceAll,
			expectVer:  edit.ExpectVersion,
		})
	}

	sem := make(chan struct{}, toolutil.IOWorkers())
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]fileResult, len(order))
	contents := make(map[int]string)
	changed := make(map[int]bool)

	for i, path := range order {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		edits := fileEdits[path]
		sem <- struct{}{}
		wg.Add(1)

		go func(idx int, p string, ed []fileEdit) {
			defer func() { <-sem; wg.Done() }()

			// Binary protection
			if toolutil.IsBinaryFile(p) {
				mu.Lock()
				results[idx] = fileResult{Path: p, Status: "failed", Error: "cannot edit binary file"}
				mu.Unlock()
				return
			}

			// Version guard (DSH fs-observation-policy): an expect_version
			// on any edit of this file must still match — the file changed
			// since the caller's read, so the edit is rejected, not applied
			// blind.
			for _, e := range ed {
				if e.expectVer != "" {
					if err := toolutil.CheckVersion(p, e.expectVer); err != nil {
						mu.Lock()
						results[idx] = fileResult{Path: p, Status: "failed", Error: err.Error()}
						mu.Unlock()
						return
					}
					break // one verified expectation suffices per file
				}
			}

			data, err := os.ReadFile(p)
			if err != nil {
				mu.Lock()
				results[idx] = fileResult{Path: p, Status: "failed", Error: fmt.Sprintf("read error: %v", err)}
				mu.Unlock()
				return
			}

			content := string(data)
			fileChanged := false

			for _, e := range ed {
				var newContent string
				replaceAll := args.ReplaceAll || e.replaceAll

				if args.Regex {
					re, err := regexp.Compile(e.old)
					if err != nil {
						mu.Lock()
						results[idx] = fileResult{Path: p, Status: "failed", Error: fmt.Sprintf("invalid regex %q: %v", e.old, err)}
						mu.Unlock()
						return
					}
					if !re.MatchString(content) {
						mu.Lock()
						results[idx] = fileResult{Path: p, Status: "failed", Error: fmt.Sprintf("pattern %q not found", e.old)}
						mu.Unlock()
						return
					}
					if replaceAll {
						newContent = re.ReplaceAllString(content, e.new)
					} else {
						loc := re.FindStringIndex(content)
						if loc == nil {
							mu.Lock()
							results[idx] = fileResult{Path: p, Status: "failed", Error: fmt.Sprintf("pattern %q not found", e.old)}
							mu.Unlock()
							return
						}
						newContent = content[:loc[0]] + e.new + content[loc[1]:]
					}
				} else {
					count := strings.Count(content, e.old)
					if count == 0 {
						mu.Lock()
						results[idx] = fileResult{Path: p, Status: "failed", Error: fmt.Sprintf("old_str %q not found", e.old)}
						mu.Unlock()
						return
					}
					if !replaceAll && count > 1 {
						mu.Lock()
						results[idx] = fileResult{Path: p, Status: "failed", Error: fmt.Sprintf("old_str %q found %d times (set replace_all=true or make it unique)", e.old, count)}
						mu.Unlock()
						return
					}
					if replaceAll {
						newContent = strings.ReplaceAll(content, e.old, e.new)
					} else {
						newContent = strings.Replace(content, e.old, e.new, 1)
					}
				}
				if newContent != content {
					fileChanged = true
				}
				content = newContent
			}

			mu.Lock()
			if args.Preview && fileChanged {
				results[idx] = fileResult{
					Path:    p,
					Status:  "success",
					Preview: generatePreview(string(data), content, p),
				}
			} else {
				results[idx] = fileResult{Path: p, Status: "success"}
			}
			contents[idx] = content
			changed[idx] = fileChanged
			mu.Unlock()
		}(i, path, edits)
	}

	wg.Wait()

	total := len(order)
	success := 0
	failed := 0
	for _, r := range results {
		if r.Status == "success" {
			success++
		} else {
			failed++
		}
	}

	noWrite := args.DryRun || args.Preview
	if noWrite {
		data := map[string]any{
			"results": results,
			"total":   total,
			"success": success,
			"failed":  failed,
		}
		if args.DryRun {
			data["dry_run"] = true
		}
		if args.Preview {
			data["preview"] = true
		}
		return toolutil.FormatResult(
			fmt.Sprintf("processed %d files (%d succeeded, %d failed)", total, success, failed), data), nil
	}

	// Write modified files
	for i, r := range results {
		if r.Status != "success" || !changed[i] {
			continue
		}
		if err := os.WriteFile(r.Path, []byte(contents[i]), toolutil.DefaultPerm()); err != nil {
			results[i] = fileResult{Path: r.Path, Status: "failed", Error: fmt.Sprintf("write error: %v", err)}
			success--
			failed++
		}
	}

	if success == 0 {
		return toolutil.FormatResult("no files were modified", map[string]any{
			"results": results,
			"total":   total,
			"success": success,
			"failed":  failed,
		}), nil
	}

	return toolutil.FormatResult(fmt.Sprintf("edited %d files (%d succeeded, %d failed)", total, success, failed), map[string]any{
		"results": results,
		"total":   total,
		"success": success,
		"failed":  failed,
	}), nil
}

// ---------------------------------------------------------------------------
// Mode B — cross-file same-text replacement via glob pattern
// ---------------------------------------------------------------------------

func (t *multiEditTool) runCrossFile(ctx context.Context, args *multiEditArgs) (string, error) {
	if args.OldStr == "" {
		return "", fmt.Errorf("multi_edit: old_str is required with pattern")
	}

	var re *regexp.Regexp
	if args.Regex {
		var err error
		re, err = regexp.Compile(args.OldStr)
		if err != nil {
			return "", fmt.Errorf("multi_edit: invalid regex %q: %w", args.OldStr, err)
		}
	}

	files, err := collectMatchingFiles(args)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return toolutil.FormatResult("no files matched the pattern", map[string]any{
			"results": []fileResult{},
			"total":   0,
			"success": 0,
			"failed":  0,
		}), nil
	}

	results, contents, hasMatch, err := t.processFiles(ctx, args, re, files)
	if err != nil {
		return "", err
	}
	return t.finalizeCrossFile(args, results, contents, hasMatch)
}

// collectMatchingFiles walks the working directory and returns the files
// matching the pattern (with include/exclude filters and skip dirs).
func collectMatchingFiles(args *multiEditArgs) ([]string, error) {
	var fileList []string
	pattern := filepath.ToSlash(args.Pattern)

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "__pycache__" || name == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		relPath := filepath.ToSlash(path)
		if args.Include != "" {
			m, _ := filepath.Match(filepath.ToSlash(args.Include), relPath)
			if !m {
				m2, _ := filepath.Match(filepath.ToSlash(args.Include), filepath.Base(relPath))
				if !m2 && !strings.Contains(relPath, filepath.ToSlash(args.Include)) {
					return nil
				}
			}
		}
		if args.Exclude != "" {
			m, _ := filepath.Match(filepath.ToSlash(args.Exclude), relPath)
			if m {
				return nil
			}
			m2, _ := filepath.Match(filepath.ToSlash(args.Exclude), filepath.Base(relPath))
			if m2 {
				return nil
			}
		}
		matched, _ := filepath.Match(pattern, relPath)
		if !matched {
			matched2, _ := filepath.Match(pattern, filepath.Base(relPath))
			if !matched2 {
				return nil
			}
		}
		fileList = append(fileList, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("multi_edit: walk error: %w", err)
	}
	return fileList, nil
}

// replaceContent applies the literal/regex replacement to one file's
// content; matched reports whether anything was replaced.
func replaceContent(args *multiEditArgs, content string, re *regexp.Regexp) (string, bool) {
	if args.Regex {
		if !re.MatchString(content) {
			return "", false
		}
		if args.ReplaceAll {
			return re.ReplaceAllString(content, args.NewStr), true
		}
		loc := re.FindStringIndex(content)
		if loc == nil {
			return "", false
		}
		return content[:loc[0]] + args.NewStr + content[loc[1]:], true
	}
	if !strings.Contains(content, args.OldStr) {
		return "", false
	}
	if args.ReplaceAll {
		return strings.ReplaceAll(content, args.OldStr, args.NewStr), true
	}
	return strings.Replace(content, args.OldStr, args.NewStr, 1), true
}

// processFiles runs the replacement across the file list with bounded
// worker concurrency.
func (t *multiEditTool) processFiles(ctx context.Context, args *multiEditArgs, re *regexp.Regexp, files []string) ([]fileResult, []string, []bool, error) {
	sem := make(chan struct{}, toolutil.IOWorkers())
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]fileResult, len(files))
	contents := make([]string, len(files))
	hasMatch := make([]bool, len(files))

	for i, path := range files {
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		sem <- struct{}{}
		wg.Add(1)

		go func(idx int, p string) {
			defer func() { <-sem; wg.Done() }()

			// Binary protection
			if toolutil.IsBinaryFile(p) {
				mu.Lock()
				results[idx] = fileResult{Path: p, Status: "failed", Error: "cannot edit binary file"}
				mu.Unlock()
				return
			}

			data, err := os.ReadFile(p)
			if err != nil {
				mu.Lock()
				results[idx] = fileResult{Path: p, Status: "failed", Error: fmt.Sprintf("read error: %v", err)}
				mu.Unlock()
				return
			}

			newContent, matched := replaceContent(args, string(data), re)

			mu.Lock()
			if args.Preview && matched {
				results[idx] = fileResult{
					Path:    p,
					Status:  "success",
					Preview: generatePreview(string(data), newContent, p),
				}
			} else {
				results[idx] = fileResult{Path: p, Status: "success"}
			}
			contents[idx] = newContent
			hasMatch[idx] = matched
			mu.Unlock()
		}(i, path)
	}
	wg.Wait()
	return results, contents, hasMatch, nil
}

// finalizeCrossFile summarizes the run, writes matched files (unless
// dry-run/preview), and renders the result.
func (t *multiEditTool) finalizeCrossFile(args *multiEditArgs, results []fileResult, contents []string, hasMatch []bool) (string, error) {
	total := len(results)
	success := 0
	failed := 0
	for _, r := range results {
		if r.Status == "success" {
			success++
		} else {
			failed++
		}
	}

	noWrite := args.DryRun || args.Preview
	if noWrite {
		data := map[string]any{
			"results": results,
			"total":   total,
			"success": success,
			"failed":  failed,
		}
		if args.DryRun {
			data["dry_run"] = true
		}
		if args.Preview {
			data["preview"] = true
		}
		return toolutil.FormatResult(
			fmt.Sprintf("processed %d files (%d succeeded, %d failed)", total, success, failed), data), nil
	}

	// Write matched files
	for i, r := range results {
		if r.Status != "success" || !hasMatch[i] {
			continue
		}
		if err := os.WriteFile(r.Path, []byte(contents[i]), toolutil.DefaultPerm()); err != nil {
			results[i] = fileResult{Path: r.Path, Status: "failed", Error: fmt.Sprintf("write error: %v", err)}
			success--
			failed++
		}
	}

	if success == 0 {
		return toolutil.FormatResult("no files were modified", map[string]any{
			"results": results,
			"total":   total,
			"success": success,
			"failed":  failed,
		}), nil
	}

	return toolutil.FormatResult(fmt.Sprintf("edited %d files (%d succeeded, %d failed)", total, success, failed), map[string]any{
		"results": results,
		"total":   total,
		"success": success,
		"failed":  failed,
	}), nil
}

// ---------------------------------------------------------------------------
// generatePreview — unified-diff-style preview between original and modified
// ---------------------------------------------------------------------------

func generatePreview(origContent, newContent, path string) string {
	if origContent == newContent {
		return "(no changes)"
	}

	origLines := strings.Split(origContent, "\n")
	newLines := strings.Split(newContent, "\n")

	if len(origLines) > 0 && origLines[len(origLines)-1] == "" {
		origLines = origLines[:len(origLines)-1]
	}
	if len(newLines) > 0 && newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}

	first := 0
	for first < len(origLines) && first < len(newLines) && origLines[first] == newLines[first] {
		first++
	}
	if first == len(origLines) && first == len(newLines) {
		return "(no changes)"
	}

	lo := len(origLines) - 1
	ln := len(newLines) - 1
	for lo >= first && ln >= first && origLines[lo] == newLines[ln] {
		lo--
		ln--
	}

	ctxBefore := first - 2
	if ctxBefore < 0 {
		ctxBefore = 0
	}
	ctxAfter := lo + 2
	if ctxAfter >= len(origLines) {
		ctxAfter = len(origLines) - 1
	}

	var b strings.Builder
	b.WriteString("--- " + path + "\n")
	b.WriteString("+++ " + path + "\n")
	for i := ctxBefore; i < first; i++ {
		b.WriteString(" " + origLines[i] + "\n")
	}
	for i := first; i <= lo; i++ {
		b.WriteString("-" + origLines[i] + "\n")
	}
	for i := first; i <= ln; i++ {
		b.WriteString("+" + newLines[i] + "\n")
	}
	for i := lo + 1; i <= ctxAfter; i++ {
		b.WriteString(" " + origLines[i] + "\n")
	}
	return b.String()
}

// AllTools returns all tools in this package.
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
