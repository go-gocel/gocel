package glob

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/internal/toolutil"
)

// Provider is the tool provider that exposes the glob tool.
// Provider 是提供 glob 工具的工具提供者。
type Provider struct{}

// New creates a new glob provider.
// New 创建新的 glob 工具提供者。
func New() *Provider { return &Provider{} }

// ListTools returns the tools provided by this provider.
// ListTools 返回该提供者提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{&globTool{}}
}

type globTool struct{}

// Name returns the tool name "glob".
// Name 返回工具名 "glob"。
func (t *globTool) Name() string { return "glob" }
// Description returns the tool description.
// Description 返回工具描述。
func (t *globTool) Description() string {
	return "Find files matching a glob pattern (e.g. \"**/*.go\"). Supports extension/depth filters and respects .gitignore by default."
}
// Schema returns the JSON schema of the tool arguments.
// Schema 返回工具参数的 JSON schema。
func (t *globTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":         map[string]any{"type": "string", "description": "Glob pattern (e.g. \"**/*.go\", \"src/**/*.ts\")"},
			"path":            map[string]any{"type": "string", "description": "Base directory to search in (default: current)"},
			"extension":       map[string]any{"type": "string", "description": "Extension filter (e.g. \"go\", \"ts\")"},
			"max_depth":       map[string]any{"type": "integer", "description": "Maximum directory depth (0=unlimited)"},
			"no_ignore":       map[string]any{"type": "boolean", "description": "Do not respect .gitignore"},
			"follow_symlinks": map[string]any{"type": "boolean", "description": "Follow symbolic links (default false)"},
			"max_results":     map[string]any{"type": "integer", "description": "Maximum number of results (default 100, 0=unlimited)"},
		},
		"required": []string{"pattern"},
	}
}

// ToolMeta returns the metadata of the tool.
// ToolMeta 返回工具元数据。
func (t *globTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }

type globArgs struct {
	Pattern        string `json:"pattern"`
	Path           string `json:"path"`
	Extension      string `json:"extension"`
	MaxDepth       int    `json:"max_depth"`
	NoIgnore       bool   `json:"no_ignore"`
	FollowSymlinks bool   `json:"follow_symlinks"`
	MaxResults     int    `json:"max_results"`
}

type fileInfo struct {
	path    string
	modTime time.Time
	size    int64
	isDir   bool
	isSym   bool
}

// Run executes a glob search with the given JSON args and returns the matched files.
// Run 根据给定的 JSON 参数执行 glob 搜索并返回匹配的文件。
func (t *globTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args globArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("glob: invalid args: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("glob: pattern is required")
	}

	basePath := args.Path
	if basePath == "" {
		basePath = "."
	}

	pattern := args.Pattern
	if !strings.HasPrefix(pattern, basePath) {
		pattern = filepath.Join(basePath, pattern)
	}

	maxResults := args.MaxResults
	if maxResults < 0 {
		maxResults = 100
	}

	extFilter := ""
	if args.Extension != "" {
		extFilter = strings.ToLower(strings.TrimPrefix(args.Extension, "."))
	}

	ignorePatterns := loadGitignore(basePath)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	maxWorkers := toolutil.IOWorkers()

	var mu sync.Mutex
	var files []fileInfo
	fileCount := 0

	if strings.Contains(args.Pattern, "**") || args.Extension != "" || args.MaxDepth > 0 || args.FollowSymlinks {
		matchPattern := args.Pattern
		matchPattern = strings.TrimPrefix(matchPattern, "./")

		var walkWg sync.WaitGroup
		walkWg.Add(1)

		fileCh := make(chan string, maxWorkers*4)

		go func() {
			defer walkWg.Done()
			defer close(fileCh)

			filepath.WalkDir(basePath, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}

				if d.IsDir() {
					name := d.Name()
					if strings.HasPrefix(name, ".") && name != "." && name != ".." {
						return filepath.SkipDir
					}
					if toolutil.IsSkippedDir(name) {
						return filepath.SkipDir
					}
					if !args.NoIgnore && isIgnoredDir(path, name, ignorePatterns, basePath) {
						return filepath.SkipDir
					}
					return nil
				}

				if strings.HasPrefix(d.Name(), ".") {
					return nil
				}

				relPath, relErr := filepath.Rel(basePath, path)
				if relErr != nil {
					return nil
				}

				if !toolutil.GlobMatch(matchPattern, relPath) {
					return nil
				}

				if args.MaxDepth > 0 {
					depth := len(strings.Split(relPath, string(filepath.Separator)))
					if depth > args.MaxDepth {
						return nil
					}
				}

				select {
				case fileCh <- path:
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			})
		}()

		var workerWg sync.WaitGroup
		for i := 0; i < maxWorkers; i++ {
			workerWg.Add(1)
			go func() {
				defer workerWg.Done()
				for path := range fileCh {
					info, err := os.Lstat(path)
					if err != nil {
						continue
					}

					if extFilter != "" && strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) != extFilter {
						continue
					}

					if args.FollowSymlinks && info.Mode()&os.ModeSymlink != 0 {
						fi, err := os.Stat(path)
						if err != nil {
							continue
						}
						info = fi
					}

					fi := fileInfo{
						path:    path,
						modTime: info.ModTime(),
						size:    info.Size(),
						isDir:   info.IsDir(),
						isSym:   info.Mode()&os.ModeSymlink != 0,
					}

					mu.Lock()
					files = append(files, fi)
					fileCount++
					if maxResults > 0 && fileCount >= maxResults {
						mu.Unlock()
						cancel()
						break
					}
					mu.Unlock()
				}
			}()
		}

		walkWg.Wait()
		workerWg.Wait()
	} else {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", fmt.Errorf("glob: invalid pattern: %w", err)
		}

		files = make([]fileInfo, 0, len(matches))
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil {
				continue
			}
			files = append(files, fileInfo{
				path:    m,
				modTime: info.ModTime(),
				size:    info.Size(),
			})
		}
	}

	if len(files) == 0 {
		return toolutil.FormatResult("No files matched the pattern.", map[string]any{
			"files": []string{},
			"total": 0,
		}), nil
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].path < files[j].path
	})

	if maxResults > 0 && len(files) > maxResults {
		files = files[:maxResults]
	}

	filtered := make([]string, len(files))
	for i, f := range files {
		filtered[i] = f.path
	}
	return toolutil.FormatResult(fmt.Sprintf("found %d matching files", len(filtered)), map[string]any{
		"files": filtered,
		"total": len(filtered),
	}), nil
}

func loadGitignore(basePath string) []string {
	data, err := os.ReadFile(filepath.Join(basePath, ".gitignore"))
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

func isIgnoredDir(path, name string, patterns []string, basePath string) bool {
	if len(patterns) == 0 {
		return false
	}
	rel, err := filepath.Rel(basePath, path)
	if err != nil {
		return false
	}
	for _, p := range patterns {
		if strings.HasPrefix(p, "/") {
			p = p[1:]
		}
		p = strings.TrimSuffix(p, "/")
		if matched, _ := filepath.Match(p, name); matched {
			return true
		}
		if matched, _ := filepath.Match(p, rel); matched {
			return true
		}
		if strings.HasPrefix(rel, p+string(filepath.Separator)) || rel == p {
			return true
		}
	}
	return false
}

// AllTools returns the glob tool.
// AllTools 返回 glob 工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
