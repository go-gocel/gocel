package grep

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/internal/toolutil"
)

// Provider is the tool provider that exposes the grep tool.
// Provider 是提供 grep 工具的工具提供者。
type Provider struct{}

// New creates a new grep provider.
// New 创建新的 grep 工具提供者。
func New() *Provider { return &Provider{} }

// ListTools returns the tools provided by this provider.
// ListTools 返回该提供者提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{&grepTool{}}
}

type grepTool struct{}

// Name returns the tool name "grep".
// Name 返回工具名 "grep"。
func (t *grepTool) Name() string { return "grep" }
// Description returns the tool description.
// Description 返回工具描述。
func (t *grepTool) Description() string {
	return "Search file contents under a directory. Regex by default; supports literal mode, context lines, case control, glob/type filters, .gitignore."
}
// Schema returns the JSON schema of the tool arguments.
// Schema 返回工具参数的 JSON schema。
func (t *grepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":        map[string]any{"type": "string", "description": "Pattern to search for (regex by default; literal=true for plain text)"},
			"path":           map[string]any{"type": "string", "description": "Directory or file to search (default: current directory)"},
			"context":        map[string]any{"type": "integer", "description": "Lines of context around each match (default 0)"},
			"before_context": map[string]any{"type": "integer", "description": "Context lines before each match (default 0)"},
			"after_context":  map[string]any{"type": "integer", "description": "Context lines after each match (default 0)"},
			"max_results":    map[string]any{"type": "integer", "description": "Maximum results to return (default 50, 0=unlimited)"},
			"literal":        map[string]any{"type": "boolean", "description": "Treat pattern as plain text (not regex)"},
			"ignore_case":    map[string]any{"type": "boolean", "description": "Case-insensitive search (default: auto when pattern is all lowercase)"},
			"include":        map[string]any{"type": "string", "description": "Only files matching this glob (e.g. \"*.go\", \"src/**/*.ts\")"},
			"exclude":        map[string]any{"type": "string", "description": "Skip files matching this glob"},
			"type":           map[string]any{"type": "string", "description": "File type to include (e.g. \"go\", \"rs\", \"py\")"},
			"no_ignore":      map[string]any{"type": "boolean", "description": "Search files ignored by .gitignore too"},
			"hidden":         map[string]any{"type": "boolean", "description": "Include hidden files and directories"},
			"max_depth":      map[string]any{"type": "integer", "description": "Maximum directory depth (0=unlimited)"},
		},
		"required": []string{"pattern"},
	}
}

// ToolMeta returns the metadata of the tool.
// ToolMeta 返回工具元数据。
func (t *grepTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Context    int    `json:"context"`
	BeforeCtx  int    `json:"before_context"`
	AfterCtx   int    `json:"after_context"`
	MaxResult  int    `json:"max_results"`
	Literal    bool   `json:"literal"`
	IgnoreCase bool   `json:"ignore_case"`
	Include    string `json:"include"`
	Exclude    string `json:"exclude"`
	Type       string `json:"type"`
	NoIgnore   bool   `json:"no_ignore"`
	Hidden     bool   `json:"hidden"`
	MaxDepth   int    `json:"max_depth"`
}

type lineMatch struct {
	LineNum int    `json:"line_number"`
	Line    string `json:"line"`
	Type    string `json:"type"` // "match", "before", "after"
}

type fileMatches struct {
	Path    string      `json:"path"`
	Matches []lineMatch `json:"matches"`
	Count   int         `json:"count,omitempty"`
}

// Run executes a grep search with the given JSON args and returns matching lines.
// Run 根据给定的 JSON 参数执行 grep 搜索并返回匹配行。
func (t *grepTool) Run(ctx context.Context, argsJSON string) (string, error) {
	o, err := parseGrepArgs(argsJSON)
	if err != nil {
		return "", err
	}
	searchPath := o.args.Path
	if searchPath == "" {
		searchPath = "."
	}

	info, err := os.Stat(searchPath)
	if err == nil && !info.IsDir() {
		return t.grepSingleFile(o, searchPath)
	}
	return t.grepDirectory(ctx, o, searchPath)
}
func compilePattern(pattern string, literal, ignoreCase bool) (*regexp.Regexp, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		if literal {
			pattern = regexp.QuoteMeta(pattern)
		} else {
			return nil, fmt.Errorf("grep: invalid regex pattern %q: %w", pattern, err)
		}
	} else if !ignoreCase {
		return re, nil
	}

	if ignoreCase {
		pattern = "(?i)" + pattern
	}

	re, err = regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("grep: invalid pattern %q: %w", pattern, err)
	}
	return re, nil
}

type lineInfo struct {
	num  int
	text string
}

func searchFile(path string, re *regexp.Regexp, beforeCtx, afterCtx, maxResults int) (fileMatches, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileMatches{Path: path}, fmt.Errorf("grep: opening %q: %w", path, err)
	}
	defer f.Close()

	var lines []lineInfo
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		lines = append(lines, lineInfo{lineNum, scanner.Text()})
	}

	if err := scanner.Err(); err != nil {
		return fileMatches{}, fmt.Errorf("grep: reading %q: %w", path, err)
	}

	var matches []lineMatch
	matchCount := 0
	lastMatchLine := -beforeCtx - 2

	for i, li := range lines {
		if !re.MatchString(li.text) {
			continue
		}

		if maxResults > 0 && matchCount >= maxResults {
			break
		}

		ctxStart := i - beforeCtx
		if ctxStart < 0 {
			ctxStart = 0
		}

		ctxEnd := i + afterCtx
		if ctxEnd >= len(lines) {
			ctxEnd = len(lines) - 1
		}

		if i-lastMatchLine > beforeCtx+afterCtx+1 {
			matches = append(matches, lineMatch{Type: "separator"})
		}

		for j := ctxStart; j < i; j++ {
			matches = append(matches, lineMatch{
				LineNum: lines[j].num,
				Line:    lines[j].text,
				Type:    "before",
			})
		}

		matches = append(matches, lineMatch{
			LineNum: li.num,
			Line:    li.text,
			Type:    "match",
		})

		for j := i + 1; j <= ctxEnd; j++ {
			matches = append(matches, lineMatch{
				LineNum: lines[j].num,
				Line:    lines[j].text,
				Type:    "after",
			})
		}

		matchCount++
		lastMatchLine = i
	}

	// Deduplicate: skip lines that were already output as trailing context of previous match
	cleanMatches := make([]lineMatch, 0, len(matches))
	seenLines := make(map[string]bool)
	for _, m := range matches {
		key := fmt.Sprintf("%d:%s", m.LineNum, m.Type)
		if seenLines[key] {
			continue
		}
		seenLines[key] = true
		cleanMatches = append(cleanMatches, m)
	}

	return fileMatches{
		Path:    path,
		Matches: cleanMatches,
		Count:   matchCount,
	}, nil
}

func formatText(results []fileMatches) string {
	var b strings.Builder
	for _, fm := range results {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(fm.Path)
		b.WriteString(":\n")
		for _, m := range fm.Matches {
			switch m.Type {
			case "separator":
				b.WriteString("--\n")
			case "match":
				b.WriteString(fmt.Sprintf("%d: %s\n", m.LineNum, m.Line))
			case "before":
				b.WriteString(fmt.Sprintf("%d- %s\n", m.LineNum, m.Line))
			case "after":
				b.WriteString(fmt.Sprintf("%d> %s\n", m.LineNum, m.Line))
			}
		}
	}
	return b.String()
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

func isIgnoredByGitignore(path, name string, patterns []string, basePath string) bool {
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

type globMatcher struct {
	pattern string
}

func newGlobMatcher(pattern string) *globMatcher {
	pattern = filepath.ToSlash(pattern)
	return &globMatcher{pattern: pattern}
}

func (g *globMatcher) match(path string) bool {
	// Use the shared GlobMatch for consistent ** support across all tools
	if toolutil.GlobMatch(g.pattern, path) {
		return true
	}
	// Also match against just the filename for convenience (e.g. "*.go" matches subdirs)
	base := filepath.Base(path)
	return toolutil.GlobMatch(g.pattern, base)
}

// AllTools returns all tools in this package.
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
