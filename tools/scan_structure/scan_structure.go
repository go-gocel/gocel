package scan_structure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/internal/toolutil"
)

const (
	maxLineCountFileSize = 1 << 20    // 1 MB — skip line counting beyond this
	largeFileSizeThr     = 500 * 1024 // 500 KB — reported as "large"
	largeFileLinesThr    = 1000       // 1000 lines — reported as "large"
)

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

// Provider is the tool provider that exposes the scan_structure tool.
// Provider 是提供 scan_structure 工具的工具提供者。
type Provider struct{}

// New creates a new scan_structure provider.
// New 创建新的 scan_structure 工具提供者。
func New() *Provider { return &Provider{} }

// ListTools returns the tools provided by this provider.
// ListTools 返回该提供者提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{&scanStructureTool{}}
}

// ---------------------------------------------------------------------------
// Tool metadata
// ---------------------------------------------------------------------------

type scanStructureTool struct{}

// Name returns the tool name "scan_structure".
// Name 返回工具名 "scan_structure"。
func (t *scanStructureTool) Name() string { return "scan_structure" }

// Description returns the tool description.
// Description 返回工具描述。
func (t *scanStructureTool) Description() string {
	return "Scan project structure: directory tree, lines of code by file extension, " +
		"directory hotspots, large file warnings, project-type detection, " +
		"and a compact fingerprint. Raw facts for LLM interpretation."
}

// Schema returns the JSON schema of the tool arguments.
// Schema 返回工具参数的 JSON schema。
func (t *scanStructureTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "Directory to scan (default: project root)"},
			"depth": map[string]any{"type": "integer", "description": "Directory tree depth (default 3, 0=full)"},
		},
		"required": []any{},
	}
}

// ToolMeta returns the metadata of the tool.
// ToolMeta 返回工具元数据。
func (t *scanStructureTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }

// ---------------------------------------------------------------------------
// Arguments
// ---------------------------------------------------------------------------

type scanArgs struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}

// ---------------------------------------------------------------------------
// Internal types
// ---------------------------------------------------------------------------

type fileInfo struct {
	path      string
	ext       string
	dir       string
	depth     int
	lines     int
	size      int64
	generated bool
}

type projectInfo struct {
	Type     string `json:"type"`
	Language string `json:"language"`
	Entry    string `json:"entry,omitempty"`
	Version  string `json:"version,omitempty"`
	Build    string `json:"build,omitempty"`
}

// JSON-friendly output types

type langStat struct {
	Files int `json:"files"`
	Lines int `json:"lines"`
}

type dirStat struct {
	Dir   string `json:"dir"`
	Files int    `json:"files"`
	Lines int    `json:"lines"`
	Depth int    `json:"depth"`
}

type largeFile struct {
	Path   string `json:"path"`
	Lines  int    `json:"lines"`
	SizeKB int64  `json:"size_kb"`
}

type characteristics struct {
	HasMakefile      bool `json:"has_makefile"`
	HasDockerfile    bool `json:"has_dockerfile"`
	HasCI            bool `json:"has_ci"`
	HasCmdDir        bool `json:"has_cmd_dir"`
	HasAPIDir        bool `json:"has_api_dir"`
	HasDocs          bool `json:"has_docs"`
	HasExamples      bool `json:"has_examples"`
	HasProtobuf      bool `json:"has_protobuf"`
	HasVendor        bool `json:"has_vendor"`
	HasGeneratedCode bool `json:"has_generated_code"`
	HasScripts       bool `json:"has_scripts"`
}

// ---------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------

// Run scans the project structure with the given JSON args and returns the report.
// Run 根据给定的 JSON 参数扫描项目结构并返回报告。
func (t *scanStructureTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args scanArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("scan_structure: invalid args: %w", err)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	// ----- resolve & validate path -----------------------------------------

	searchPath := args.Path
	if searchPath == "" {
		searchPath = "."
	}
	maxDepth := args.Depth
	if maxDepth < 0 {
		maxDepth = 3
	}

	absPath, err := filepath.Abs(searchPath)
	if err != nil {
		absPath = searchPath
	}
	fi, err := os.Stat(searchPath)
	if err != nil {
		return toolutil.FormatResult("", map[string]any{
			"tree":        "  (empty — path does not exist)",
			"root":        absPath,
			"total_files": 0,
			"total_dirs":  0,
		}), nil
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("scan_structure: path %q is not a directory", searchPath)
	}

	// ----- phase 1: walk directory tree ------------------------------------
	// Collects file metadata, skips noisy dirs.

	walkedFiles, dirs := walkTree(searchPath, maxDepth)

	// quick empty check
	if len(walkedFiles) == 0 && len(dirs) == 0 {
		// No-files-but-dirs case handled below; both zero is truly empty
		return toolutil.FormatResult("", map[string]any{
			"tree":        "  (empty)",
			"root":        absPath,
			"total_files": 0,
			"total_dirs":  0,
		}), nil
	}

	// ----- phases 2-10: line counts, tree, stats, fingerprint, metadata ----

	data := analyzeStructure(walkedFiles, dirs, searchPath, maxDepth, absPath)
	return toolutil.FormatResult("", data), nil
}

// analyzeStructure runs the remaining analysis phases over the walked tree
// and assembles the output document.
func analyzeStructure(walkedFiles []fileInfo, dirs map[string]bool, searchPath string, maxDepth int, absPath string) map[string]any {
	// phase 2: count lines + detect generated code (text files only,
	// under the size threshold).
	for i := range walkedFiles {
		f := &walkedFiles[i]
		if f.size > maxLineCountFileSize || !toolutil.IsTextExt(f.ext) {
			continue
		}
		lines, generated, err := countLinesAndCheckGenerated(f.path)
		if err == nil {
			f.lines = lines
			f.generated = generated
		}
	}

	// phases 3-10: tree, extension stats, dir hotspots, large files,
	// characteristics, project metadata, fingerprint.
	tree := buildTree(dirs, maxDepth)
	extStats := aggregateByExt(walkedFiles)
	dirStats := buildDirStats(walkedFiles)
	largeFiles := detectLargeFiles(walkedFiles)
	chars := detectCharacteristics(dirs, walkedFiles)
	projInfo := detectProject(searchPath)
	fingerprint := buildFingerprint(extStats, chars, projInfo)

	// total_lines = sum of all counted lines across all files
	totalLines := 0
	for _, f := range walkedFiles {
		totalLines += f.lines
	}

	data := map[string]any{
		"tree":            tree,
		"root":            absPath,
		"total_files":     len(walkedFiles),
		"total_dirs":      len(dirs),
		"total_lines":     totalLines,
		"max_depth":       maxFileDepth(walkedFiles),
		"fingerprint":     fingerprint,
		"extensions":      extStats,
		"dir_stats":       dirStats,
		"large_files":     largeFiles,
		"characteristics": chars,
	}
	if projInfo.Type != "Unknown" {
		data["project_type"] = projInfo.Type
		data["project_language"] = projInfo.Language
		if projInfo.Entry != "" {
			data["entry_point"] = projInfo.Entry
		}
		if projInfo.Version != "" {
			data["project_version"] = projInfo.Version
		}
	}
	return data
}

// ---------------------------------------------------------------------------
// Walk helpers
// ---------------------------------------------------------------------------

// walkTree collects file metadata and directory names under searchPath,
// skipping noisy directories and files beyond maxDepth+3.
func walkTree(searchPath string, maxDepth int) ([]fileInfo, map[string]bool) {
	walkedFiles := make([]fileInfo, 0)
	dirs := map[string]bool{}
	var mu sync.Mutex

	filepath.WalkDir(searchPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(searchPath, path)
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		depth := len(parts)

		if d.IsDir() {
			if toolutil.IsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			mu.Lock()
			dirs[rel] = true
			mu.Unlock()
			return nil
		}
		if depth > maxDepth+3 {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil // skip if we can't stat
		}

		ext := strings.ToLower(filepath.Ext(path))

		mu.Lock()
		walkedFiles = append(walkedFiles, fileInfo{
			path:  path,
			ext:   ext,
			dir:   filepath.Dir(rel),
			depth: depth,
			size:  info.Size(),
		})
		mu.Unlock()
		return nil
	})
	return walkedFiles, dirs
}

// countLinesAndCheckGenerated reads a text file, counts lines, and detects
// the standard Go generated-code marker.
func countLinesAndCheckGenerated(path string) (lines int, generated bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	if len(data) == 0 {
		return 0, false, nil
	}

	// Check first line for generated marker
	firstNl := bytes.IndexByte(data, '\n')
	var firstLine string
	if firstNl >= 0 {
		firstLine = string(data[:firstNl])
	} else {
		firstLine = string(data)
	}
	firstLine = strings.TrimSpace(firstLine)
	if strings.HasPrefix(firstLine, "// Code generated") ||
		strings.HasPrefix(firstLine, "// @generated") ||
		strings.Contains(firstLine, "@generated") ||
		strings.HasPrefix(firstLine, "// @formatted") {
		generated = true
	}

	// Count newlines
	lines = bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		lines++
	}
	return lines, generated, nil
}

func maxFileDepth(files []fileInfo) int {
	max := 0
	for _, f := range files {
		if f.depth > max {
			max = f.depth
		}
	}
	return max
}

// ---------------------------------------------------------------------------
// Extension stats — grouped by raw file extension (e.g. ".go", ".ts")
// ---------------------------------------------------------------------------

func aggregateByExt(files []fileInfo) map[string]langStat {
	stats := map[string]*langStat{}
	keys := make([]string, 0)

	for _, f := range files {
		ext := f.ext
		if ext == "" {
			ext = "(no ext)"
		}
		if _, ok := stats[ext]; !ok {
			stats[ext] = &langStat{}
			keys = append(keys, ext)
		}
		stats[ext].Files++
		stats[ext].Lines += f.lines
	}

	sort.Slice(keys, func(i, j int) bool {
		return stats[keys[i]].Lines > stats[keys[j]].Lines
	})

	out := map[string]langStat{}
	for _, k := range keys {
		out[k] = *stats[k]
	}
	return out
}

// ---------------------------------------------------------------------------
// Directory hotspot stats
// ---------------------------------------------------------------------------

func buildDirStats(files []fileInfo) []dirStat {
	agg := map[string]*dirStat{}
	keys := make([]string, 0)

	for _, f := range files {
		// Skip root-level files (dir == ".")
		if f.dir == "." {
			continue
		}
		if _, ok := agg[f.dir]; !ok {
			parts := strings.Split(f.dir, string(filepath.Separator))
			agg[f.dir] = &dirStat{Dir: f.dir, Depth: len(parts)}
			keys = append(keys, f.dir)
		}
		agg[f.dir].Files++
		agg[f.dir].Lines += f.lines
	}

	sort.Slice(keys, func(i, j int) bool {
		return agg[keys[i]].Lines > agg[keys[j]].Lines
	})

	out := make([]dirStat, 0, min(len(keys), 15))
	for i, k := range keys {
		if i >= 15 {
			break
		}
		out = append(out, *agg[k])
	}
	return out
}

// ---------------------------------------------------------------------------
// Large files
// ---------------------------------------------------------------------------

func detectLargeFiles(files []fileInfo) []largeFile {
	out := make([]largeFile, 0)
	for _, f := range files {
		if f.size < largeFileSizeThr && f.lines < largeFileLinesThr {
			continue
		}
		if !toolutil.IsTextExt(f.ext) {
			continue
		}
		if f.lines == 0 && f.size > largeFileSizeThr {
			// Binary or unreadable large file — still worth flagging
			out = append(out, largeFile{Path: f.path, Lines: 0, SizeKB: f.size / 1024})
			continue
		}
		out = append(out, largeFile{Path: f.path, Lines: f.lines, SizeKB: f.size / 1024})
	}
	// Sort by size descending
	sort.Slice(out, func(i, j int) bool { return out[i].SizeKB > out[j].SizeKB })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// ---------------------------------------------------------------------------
// Characteristics detection
// ---------------------------------------------------------------------------

func detectCharacteristics(dirs map[string]bool, files []fileInfo) characteristics {
	var c characteristics

	// Scan directories
	for d := range dirs {
		base := filepath.Base(d)
		switch base {
		case "cmd", "cli":
			c.HasCmdDir = true
		case "api":
			c.HasAPIDir = true
		case "docs", "documentation":
			c.HasDocs = true
		case "examples", "example":
			c.HasExamples = true
		case "vendor":
			c.HasVendor = true
		case "scripts", "hack":
			c.HasScripts = true
		case ".github", ".gitlab":
			c.HasCI = true
		}
	}

	// Scan files
	for _, f := range files {
		base := filepath.Base(f.path)
		switch base {
		case "Makefile", "makefile", "GNUmakefile":
			c.HasMakefile = true
		case "Dockerfile", "dockerfile", "Containerfile":
			c.HasDockerfile = true
		case "Jenkinsfile", ".gitlab-ci.yml":
			c.HasCI = true
		}
		if f.ext == ".proto" {
			c.HasProtobuf = true
		}
		if f.generated {
			c.HasGeneratedCode = true
		}
	}
	return c
}

// ---------------------------------------------------------------------------
// Fingerprint — compact one-line summary for LLM quick-scan
// Format: {ext}|{build}|{files}|{loc}|{tags}
// ---------------------------------------------------------------------------

func buildFingerprint(exts map[string]langStat, chars characteristics, proj projectInfo) string {
	// Primary extension = the one with most lines
	primaryExt := "?"
	topLines := 0
	totalFiles := 0
	totalLines := 0
	for ext, s := range exts {
		totalFiles += s.Files
		totalLines += s.Lines
		if s.Lines > topLines {
			topLines = s.Lines
			primaryExt = ext
		}
	}

	build := proj.Build

	tags := make([]string, 0, 6)
	if chars.HasMakefile {
		tags = append(tags, "make")
	}
	if chars.HasDockerfile {
		tags = append(tags, "docker")
	}
	if chars.HasCI {
		tags = append(tags, "ci")
	}
	if chars.HasAPIDir {
		tags = append(tags, "api")
	}
	if chars.HasCmdDir {
		tags = append(tags, "cli")
	}
	if chars.HasProtobuf {
		tags = append(tags, "proto")
	}
	if chars.HasVendor {
		tags = append(tags, "vendor")
	}
	if chars.HasGeneratedCode {
		tags = append(tags, "gen")
	}
	if chars.HasScripts {
		tags = append(tags, "scripts")
	}

	tagStr := ""
	if len(tags) > 0 {
		tagStr = "|" + strings.Join(tags, ",")
	}

	var locStr string
	if totalLines >= 1_000_000 {
		locStr = fmt.Sprintf("%.1fMLOC", float64(totalLines)/1_000_000)
	} else if totalLines >= 1000 {
		locStr = fmt.Sprintf("%dKLOC", totalLines/1000)
	} else {
		locStr = fmt.Sprintf("%dLOC", totalLines)
	}

	return fmt.Sprintf("%s|%s|%d files|%s%s",
		primaryExt, build, totalFiles, locStr, tagStr)
}

// ---------------------------------------------------------------------------
// Project detection
// ---------------------------------------------------------------------------

func detectProject(base string) projectInfo {
	checks := []struct {
		file     string
		projType string
		lang     string
		entry    string
		version  string
		build    string
	}{
		{"go.mod", "Go Module", "Go", "main.go", "", "go build"},
		{"Cargo.toml", "Cargo Project", "Rust", "src/main.rs", "", "cargo"},
		{"package.json", "npm Package", "JavaScript", "index.js", "", "npm"},
		{"pyproject.toml", "Python Project", "Python", "", "", "pip"},
		{"setup.py", "Python Project", "Python", "", "", "pip"},
		{"Gemfile", "Ruby Project", "Ruby", "", "", "bundle"},
		{"composer.json", "PHP Project", "PHP", "", "", "composer"},
		{"CMakeLists.txt", "CMake Project", "C/C++", "", "", "cmake"},
		{"build.gradle", "Gradle Project", "Java/Kotlin", "", "", "gradle"},
		{"pom.xml", "Maven Project", "Java", "", "", "mvn"},
		{"mix.exs", "Elixir Project", "Elixir", "", "", "mix"},
	}

	for _, c := range checks {
		data, err := os.ReadFile(filepath.Join(base, c.file))
		if err != nil {
			continue
		}
		info := projectInfo{Type: c.projType, Language: c.lang, Build: c.build}

		switch c.file {
		case "go.mod":
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "module ") {
					info.Entry = strings.TrimSpace(strings.TrimPrefix(line, "module "))
					break
				}
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "go ") {
					info.Version = strings.TrimSpace(strings.TrimPrefix(line, "go "))
					break
				}
			}
		case "package.json":
			var parsed struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}
			if json.Unmarshal(data, &parsed) == nil {
				info.Entry = parsed.Name
				info.Version = parsed.Version
			}
		case "Cargo.toml":
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name = ") {
					info.Entry = strings.Trim(strings.TrimPrefix(line, "name = "), "\"")
				}
				if strings.HasPrefix(line, "version = ") {
					info.Version = strings.Trim(strings.TrimPrefix(line, "version = "), "\"")
					break
				}
			}
		}
		return info
	}

	return projectInfo{Type: "Unknown", Language: "Unknown"}
}

// ---------------------------------------------------------------------------
// Directory tree
// ---------------------------------------------------------------------------

func buildTree(dirs map[string]bool, maxDepth int) string {
	if len(dirs) == 0 {
		return "  (empty)"
	}

	sorted := make([]string, 0, len(dirs))
	for d := range dirs {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)

	filtered := make([]string, 0)
	for _, d := range sorted {
		parts := strings.Split(d, string(filepath.Separator))
		if len(parts) > maxDepth {
			continue
		}
		redundant := false
		for _, f := range filtered {
			if strings.HasPrefix(d, f+string(filepath.Separator)) {
				redundant = true
				break
			}
		}
		if !redundant {
			filtered = append(filtered, d)
		}
	}

	var b strings.Builder
	for i, d := range filtered {
		parts := strings.Split(d, string(filepath.Separator))
		indent := strings.Repeat("  ", len(parts)-1)

		isLastSibling := func(path string, entries []string) bool {
			parentDir := filepath.Dir(path)
			for j := i + 1; j < len(entries); j++ {
				if filepath.Dir(entries[j]) == parentDir {
					return false
				}
			}
			return true
		}

		connector := "├── "
		if isLastSibling(d, filtered) {
			connector = "└── "
		}
		b.WriteString(fmt.Sprintf("%s%s%s/\n", indent, connector, parts[len(parts)-1]))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// AllTools returns all tools in this package.
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
