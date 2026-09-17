package read

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/internal/toolutil"
)

const skeletonThreshold = 500 // show skeleton for files with >500 lines
const maxSkeletonLines = 100  // max lines in skeleton representation

// Provider is the tool provider that exposes the read tool.
// Provider 是提供 read 工具的工具提供者。
type Provider struct{}

// New creates a new read provider.
// New 创建新的 read 工具提供者。
func New() *Provider { return &Provider{} }

// ListTools returns the tools provided by this provider.
// ListTools 返回该提供者提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{&readFileTool{}}
}

type readFileTool struct{}

// Name returns the tool name "read".
// Name 返回工具名 "read"。
func (t *readFileTool) Name() string { return "read" }
// Description returns the tool description.
// Description 返回工具描述。
func (t *readFileTool) Description() string {
	return "Read file contents. Use symbol to read a specific function/type/method body directly. Use offset/limit to read line ranges. Skeleton mode for large files."
}
// Schema returns the JSON schema of the tool arguments.
// Schema 返回工具参数的 JSON schema。
func (t *readFileTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "Path to the file to read"},
			"symbol": map[string]any{"type": "string", "description": "Function/type/method name to extract body (e.g. \"handleRequest\", \"Config\")"},
			"offset": map[string]any{"type": "integer", "description": "Starting line number (1-based). Omit to read from beginning."},
			"limit":  map[string]any{"type": "integer", "description": "Max lines to read. Omit for full file or skeleton."},
			"force":  map[string]any{"type": "boolean", "description": "Force full file read even for large files (bypass skeleton)"},
		},
		"required": []string{"path"},
	}
}
// ToolMeta returns the metadata of the tool.
// ToolMeta 返回工具元数据。
func (t *readFileTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }

// readArgs holds the parsed input parameters for the read_file tool.
type readArgs struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
	Force  bool   `json:"force"`
}

// Run reads the file described by the given JSON args and returns its contents.
// Run 根据给定的 JSON 参数读取文件并返回其内容。
func (t *readFileTool) Run(ctx context.Context, argsJSON string) (string, error) {
	args, err := parseReadArgs(argsJSON)
	if err != nil {
		return toolutil.FormatError(err), err
	}
	lines, totalLines, err := validateAndReadFile(args.Path)
	if err != nil {
		return toolutil.FormatError(err), err
	}
	if args.Symbol != "" {
		result, err := extractSymbol(args.Path, args.Symbol, lines, totalLines)
		if err != nil {
			return toolutil.FormatError(err), err
		}
		return toolutil.FormatResult(result, map[string]any{
			"path":        args.Path,
			"total_lines": totalLines,
			"content":     result,
			"skeleton":    false,
		}), nil
	}
	isSkeleton := args.Offset <= 0 && args.Limit <= 0 && totalLines > skeletonThreshold && !args.Force
	result := formatFileOutput(args.Path, args.Offset, args.Limit, args.Force, lines, totalLines)
	// The version fingerprint enables read-before-edit: write/multiedit
	// accept it as expect_version and reject the edit if the file changed
	// since this read (DSH fs-observation-policy). Best-effort: a stat
	// failure omits the field rather than failing the read.
	meta := map[string]any{
		"path":        args.Path,
		"total_lines": totalLines,
		"content":     result,
		"skeleton":    isSkeleton,
	}
	if v, err := toolutil.FileVersion(args.Path); err == nil {
		meta["version"] = v
	}
	return toolutil.FormatResult(fmt.Sprintf("read %s (%d lines)", args.Path, totalLines), meta), nil
}

// parseReadArgs unmarshals and validates the input JSON arguments.
func parseReadArgs(argsJSON string) (readArgs, error) {
	var args readArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return args, fmt.Errorf("read_file: invalid args: %w", err)
	}
	if args.Path == "" {
		return args, fmt.Errorf("read_file: path is required")
	}
	return args, nil
}

// validateAndReadFile checks file existence, type, then reads and splits content.
func validateAndReadFile(path string) ([]string, int, error) {
	if err := validatePath(path); err != nil {
		return nil, 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read_file: read: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, len(lines), nil
}

// validatePath checks that the path exists, is not a directory, and is not a binary file.
func validatePath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if suggestion := findSimilarFile(path); suggestion != "" {
				return fmt.Errorf("read_file: file %q not found. Did you mean %q?", path, suggestion)
			}
			return fmt.Errorf("read_file: file %q not found", path)
		}
		return fmt.Errorf("read_file: stat: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("read_file: %q is a directory, not a file. Use glob (pattern \"*\") to list its contents.", path)
	}
	if toolutil.IsBinaryFile(path) {
		return fmt.Errorf("read_file: %q appears to be a binary file. Use terminal (ls -l) to check its size.", path)
	}
	return nil
}

func extractSymbol(path, symbol string, lines []string, totalLines int) (string, error) {
	symLower := strings.ToLower(symbol)
	defLine := -1
	ext := strings.ToLower(filepath.Ext(path))

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if !strings.Contains(lower, symLower) {
			continue
		}
		isDef := false
		switch ext {
		case ".go":
			if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "func (") ||
				strings.HasPrefix(trimmed, "type ") {
				isDef = true
			}
		case ".rs":
			if strings.HasPrefix(trimmed, "fn ") || strings.HasPrefix(trimmed, "pub fn ") ||
				strings.HasPrefix(trimmed, "struct ") || strings.HasPrefix(trimmed, "pub struct ") ||
				strings.HasPrefix(trimmed, "enum ") || strings.HasPrefix(trimmed, "pub enum ") ||
				strings.HasPrefix(trimmed, "trait ") || strings.HasPrefix(trimmed, "pub trait ") ||
				strings.HasPrefix(trimmed, "impl") {
				isDef = true
			}
		case ".py":
			if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "class ") {
				isDef = true
			}
		case ".ts", ".tsx", ".js", ".jsx":
			if strings.HasPrefix(trimmed, "function ") || strings.HasPrefix(trimmed, "async function ") ||
				strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "interface ") ||
				strings.HasPrefix(trimmed, "const ") && strings.Contains(trimmed, "=>") {
				isDef = true
			}
		case ".java", ".kt", ".kts", ".swift":
			if (strings.HasPrefix(trimmed, "public ") || strings.HasPrefix(trimmed, "private ") ||
				strings.HasPrefix(trimmed, "protected ") || strings.HasPrefix(trimmed, "internal ") ||
				strings.HasPrefix(trimmed, "fun ") || strings.HasPrefix(trimmed, "func ")) &&
				strings.Contains(trimmed, "(") {
				isDef = true
			}
			if strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "interface ") ||
				strings.HasPrefix(trimmed, "enum ") || strings.HasPrefix(trimmed, "struct ") {
				isDef = true
			}
		default:
			for _, prefix := range []string{"func ", "def ", "class ", "fn ", "function ", "sub ", "struct "} {
				if strings.HasPrefix(trimmed, prefix) {
					isDef = true
					break
				}
			}
		}
		if isDef {
			defLine = i
			break
		}
	}

	if defLine == -1 {
		return "", fmt.Errorf("read_file: symbol %q not found in %s", symbol, path)
	}

	bodyStart := defLine
	braceStart := -1
	for j := defLine; j < len(lines); j++ {
		if strings.Contains(lines[j], "{") {
			braceStart = j
			break
		}
	}

	if braceStart == -1 {
		// Single-line definition (e.g. Go type alias, const)
		return renderSection(path, defLine+1, 1, lines, totalLines), nil
	}

	depth := 0
	bodyEnd := braceStart
	for j := braceStart; j < len(lines); j++ {
		for _, ch := range lines[j] {
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
			}
		}
		if depth == 0 {
			bodyEnd = j
			break
		}
	}
	if depth != 0 {
		bodyEnd = len(lines) - 1
	}

	for bodyStart > 0 && strings.TrimSpace(lines[bodyStart-1]) == "" {
		bodyStart--
	}

	return renderSection(path, bodyStart+1, bodyEnd-bodyStart+1, lines, totalLines), nil
}

// formatFileOutput decides and renders the output strategy: section, skeleton, or full file.
func formatFileOutput(path string, offset, limit int, force bool, lines []string, totalLines int) string {
	if offset > 0 || limit > 0 {
		return renderSection(path, offset, limit, lines, totalLines)
	}
	if totalLines > skeletonThreshold && !force {
		return generateSkeleton(path, lines, totalLines)
	}
	return renderFullFile(path, lines, totalLines)
}

// renderSection outputs a specific line range of the file.
func renderSection(path string, offset, limit int, lines []string, totalLines int) string {
	start := offset
	if start < 1 {
		start = 1
	}
	end := totalLines
	if limit > 0 && start-1+limit < end {
		end = start - 1 + limit
	}
	if start > totalLines {
		return fmt.Sprintf("read_file: offset %d exceeds file length (%d lines)", offset, totalLines)
	}
	selected := lines[start-1 : end]
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s (%d total lines, showing lines %d-%d)\n\n", path, totalLines, start, end)
	for i, line := range selected {
		lineNum := start + i
		b.WriteString(strconv.Itoa(lineNum))
		b.WriteString("→")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// renderFullFile outputs the entire file content with line numbers.
func renderFullFile(path string, lines []string, totalLines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s (%d lines)\n\n", path, totalLines)
	for i, line := range lines {
		lineNum := i + 1
		b.WriteString(strconv.Itoa(lineNum))
		b.WriteString("→")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// generateSkeleton creates a structural overview of the file
func generateSkeleton(path string, lines []string, totalLines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s (%d lines, showing skeleton — use offset/limit or force=true to read specific sections)\n\n", path, totalLines)

	ext := strings.ToLower(filepath.Ext(path))
	sections := collectSections(lines, ext)

	if len(sections) == 0 {
		return fallbackSkeleton(lines, totalLines, &b)
	}

	renderSkeleton(&b, path, totalLines, sections)
	return b.String()
}

// section represents a single structural element in the skeleton view.
type section struct {
	lineNum int
	text    string
	kind    string // "import", "func", "type", "struct", "interface", etc.
}

// collectSections scans lines and returns structural elements for the given file extension.
func collectSections(lines []string, ext string) []section {
	handlers := map[string]func(string, int, string) *section{
		".go":   collectGoSection,
		".rs":   collectRustSection,
		".py":   collectPythonSection,
		".ts":   collectTSSection,
		".tsx":  collectTSSection,
		".js":   collectTSSection,
		".jsx":  collectTSSection,
		".java": collectJavaSection,
	}

	handler := handlers[ext]
	if handler != nil {
		return collectWithHandler(lines, handler)
	}
	return collectGenericSections(lines)
}

// collectWithHandler runs the given handler on each line and collects non-nil results.
func collectWithHandler(lines []string, fn func(string, int, string) *section) []section {
	var sections []section
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if s := fn(trimmed, i, lines[i]); s != nil {
			sections = append(sections, *s)
		}
	}
	return sections
}

func collectGoSection(trimmed string, _ int, _ string) *section {
	switch {
	case strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "func ("):
		return &section{kind: "func", text: trimmed}
	case strings.HasPrefix(trimmed, "type "):
		return &section{kind: "type", text: trimmed}
	case strings.HasPrefix(trimmed, "import") && trimmed != "import (":
		return &section{kind: "import", text: trimmed}
	}
	return nil
}

func collectRustSection(trimmed string, _ int, line string) *section {
	switch {
	case strings.HasPrefix(trimmed, "fn "):
		return &section{kind: "func", text: trimmed}
	case strings.HasPrefix(trimmed, "struct "):
		return &section{kind: "struct", text: trimmed}
	case strings.HasPrefix(trimmed, "enum "):
		return &section{kind: "enum", text: trimmed}
	case strings.HasPrefix(trimmed, "impl"):
		return &section{kind: "impl", text: trimmed}
	case strings.HasPrefix(trimmed, "trait "):
		return &section{kind: "trait", text: trimmed}
	case strings.HasPrefix(trimmed, "use "):
		return &section{kind: "import", text: trimmed}
	case strings.HasPrefix(trimmed, "mod "):
		return &section{kind: "mod", text: trimmed}
	case strings.HasPrefix(trimmed, "pub ") && (strings.Contains(trimmed, " fn ") || strings.Contains(trimmed, " struct ") || strings.Contains(trimmed, " enum ") || strings.Contains(trimmed, " trait ") || strings.Contains(trimmed, " type ")):
		return &section{kind: "pub", text: trimmed}
	}
	return nil
}

func collectPythonSection(trimmed string, _ int, _ string) *section {
	switch {
	case strings.HasPrefix(trimmed, "def "):
		return &section{kind: "def", text: trimmed}
	case strings.HasPrefix(trimmed, "class "):
		return &section{kind: "class", text: trimmed}
	case strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from "):
		return &section{kind: "import", text: trimmed}
	}
	return nil
}

func collectTSSection(trimmed string, _ int, _ string) *section {
	switch {
	case strings.HasPrefix(trimmed, "function ") || strings.HasPrefix(trimmed, "async function "):
		return &section{kind: "func", text: trimmed}
	case strings.HasPrefix(trimmed, "class "):
		return &section{kind: "class", text: trimmed}
	case strings.HasPrefix(trimmed, "interface "):
		return &section{kind: "interface", text: trimmed}
	case strings.HasPrefix(trimmed, "type ") && strings.Contains(trimmed, "="):
		return &section{kind: "type", text: trimmed}
	case strings.HasPrefix(trimmed, "export "):
		return &section{kind: "export", text: trimmed}
	case strings.HasPrefix(trimmed, "import "):
		return &section{kind: "import", text: trimmed}
	case strings.HasPrefix(trimmed, "const ") && strings.Contains(trimmed, "=>"):
		return &section{kind: "func", text: trimmed}
	}
	return nil
}

func collectJavaSection(trimmed string, _ int, _ string) *section {
	if strings.HasPrefix(trimmed, "public ") || strings.HasPrefix(trimmed, "private ") || strings.HasPrefix(trimmed, "protected ") {
		if strings.Contains(trimmed, "(") && strings.Contains(trimmed, ")") {
			return &section{kind: "func", text: trimmed}
		} else if strings.Contains(trimmed, " class ") || strings.Contains(trimmed, " interface ") || strings.Contains(trimmed, " enum ") {
			return &section{kind: "type", text: trimmed}
		}
	} else if strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "interface ") {
		return &section{kind: "type", text: trimmed}
	} else if strings.HasPrefix(trimmed, "import ") {
		return &section{kind: "import", text: trimmed}
	}
	return nil
}

func collectGenericSections(lines []string) []section {
	var sections []section
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "func") || strings.HasPrefix(trimmed, "def ") ||
			strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "type ") ||
			strings.HasPrefix(trimmed, "struct ") || strings.HasPrefix(trimmed, "interface ") ||
			strings.HasPrefix(trimmed, "enum ") || strings.HasPrefix(trimmed, "trait ") ||
			strings.HasPrefix(trimmed, "impl ") || strings.HasPrefix(trimmed, "import ") ||
			strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "export ") ||
			strings.HasPrefix(trimmed, "module ") || strings.HasPrefix(trimmed, "namespace ") ||
			strings.HasPrefix(trimmed, "#include") || strings.HasPrefix(trimmed, "#define") ||
			strings.HasPrefix(trimmed, "using ") || strings.HasPrefix(trimmed, "# ") ||
			strings.HasPrefix(trimmed, "// ") {
			kind := "section"
			for _, prefix := range []string{"func", "def ", "class", "struct", "interface", "enum", "trait", "impl", "type"} {
				if strings.HasPrefix(trimmed, prefix) {
					kind = "decl"
					break
				}
			}
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "using ") ||
				strings.HasPrefix(trimmed, "#include") || strings.HasPrefix(trimmed, "package ") {
				kind = "import"
			}
			sections = append(sections, section{lineNum: i + 1, text: trimmed, kind: kind})
		}
	}
	return sections
}

// fallbackSkeleton renders every Nth line when no structural elements are found.
func fallbackSkeleton(lines []string, totalLines int, b *strings.Builder) string {
	step := totalLines / maxSkeletonLines
	if step < 1 {
		step = 1
	}
	fmt.Fprintf(b, "[No structural elements detected — showing every %dth line]\n", step)
	for i := 0; i < totalLines; i += step {
		fmt.Fprintf(b, "  %d→ %s\n", i+1, lines[i])
	}
	return b.String()
}

// renderSkeleton writes the structural overview including summary counts and element list.
func renderSkeleton(b *strings.Builder, path string, totalLines int, sections []section) {
	lineCount := len(sections)
	if lineCount > maxSkeletonLines {
		lineCount = maxSkeletonLines
		fmt.Fprintf(b, "[Showing first %d of %d structural elements — use offset/limit to read specific sections]\n", maxSkeletonLines, len(sections))
	}

	// Summary by kind
	kindCounts := make(map[string]int)
	for _, s := range sections {
		kindCounts[s.kind]++
	}
	kindNames := map[string]string{
		"func": "functions", "def": "functions", "class": "classes",
		"struct": "structs", "interface": "interfaces", "enum": "enums",
		"trait": "traits", "impl": "impl blocks", "mod": "modules",
		"type": "types", "import": "imports", "export": "exports",
		"pub": "pub items", "section": "sections", "decl": "declarations",
	}
	var summaryParts []string
	for kind, count := range kindCounts {
		name := kindNames[kind]
		if name == "" {
			name = kind
		}
		summaryParts = append(summaryParts, fmt.Sprintf("%d %s", count, name))
	}
	fmt.Fprintf(b, "Summary: %s\n\n", strings.Join(summaryParts, ", "))

	for i := 0; i < lineCount && i < len(sections); i++ {
		s := sections[i]
		fmt.Fprintf(b, "  %d| [%s] %s\n", s.lineNum, s.kind, s.text)
	}

	if len(sections) > maxSkeletonLines {
		fmt.Fprintf(b, "\n... and %d more structural elements. Use offset/limit to read sections of interest.\n", len(sections)-maxSkeletonLines)
	}
	fmt.Fprintf(b, "\nTo read a specific section, use: read_file(path=%q, offset=<line>, limit=<count>)", path)
}

// findSimilarFile tries to find files close to the given path for "Did you mean?" suggestions
func findSimilarFile(path string) string {
	// Get the base name and dir
	base := strings.ToLower(filepath.Base(path))
	dir := filepath.Dir(path)

	// Remove extension variations for comparison
	baseName := strings.TrimSuffix(base, filepath.Ext(base))

	// Check parent directory for similar files
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Try parent of parent
		parentDir := filepath.Dir(dir)
		entries, err = os.ReadDir(parentDir)
		if err != nil {
			return ""
		}
		dir = parentDir
	}

	// Score candidates by similarity
	type candidate struct {
		name  string
		score int
	}
	var candidates []candidate

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ename := strings.ToLower(e.Name())
		enameBase := strings.TrimSuffix(ename, filepath.Ext(ename))

		// Exact base match (different extension)
		if enameBase == baseName {
			return filepath.Join(dir, e.Name())
		}

		// Prefix/suffix match
		score := 0
		if strings.HasPrefix(enameBase, baseName) || strings.HasPrefix(baseName, enameBase) {
			score += 5
		}
		if strings.Contains(enameBase, baseName) || strings.Contains(baseName, enameBase) {
			score += 3
		}

		// Levenshtein-like: common prefix length
		common := 0
		minLen := len(enameBase)
		if len(baseName) < minLen {
			minLen = len(baseName)
		}
		for i := 0; i < minLen; i++ {
			if enameBase[i] == baseName[i] {
				common++
			} else {
				break
			}
		}
		if common >= 3 {
			score += common
		}

		if score > 0 {
			candidates = append(candidates, candidate{name: e.Name(), score: score})
		}
	}

	if len(candidates) == 0 {
		return ""
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// Return best candidate if score is decent
	if candidates[0].score >= 3 {
		return filepath.Join(dir, candidates[0].name)
	}
	return ""
}

// AllTools returns all tools in this package.
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
