package scan_structure

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/internal/toolutil"
)

func makeArgs(m map[string]any) string {
	data, _ := json.Marshal(m)
	return string(data)
}

// ---------------------------------------------------------------------------
// Tool identity
// ---------------------------------------------------------------------------

func TestToolName(t *testing.T) {
	p := New()
	tools := p.ListTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "scan_structure" {
		t.Errorf("expected name 'scan_structure', got %q", tools[0].Name())
	}
}

func TestSchemaRequired(t *testing.T) {
	tool := &scanStructureTool{}
	s := tool.Schema()
	req, ok := s["required"]
	if !ok {
		t.Fatal("expected 'required' key in schema")
	}
	reqArr, ok := req.([]any)
	if !ok {
		t.Fatal("expected 'required' to be a slice")
	}
	if len(reqArr) != 0 {
		t.Errorf("expected empty required array, got %v", reqArr)
	}
}

// ---------------------------------------------------------------------------
// Empty args (scan current dir — must succeed)
// ---------------------------------------------------------------------------

func TestRun_EmptyArgs(t *testing.T) {
	tool := &scanStructureTool{}
	result, err := tool.Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", tr.Status)
	}
	data, ok := tr.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected Data to be a map, got %T", tr.Data)
	}
	for _, key := range []string{"tree", "root", "total_files", "total_dirs",
		"total_lines", "max_depth", "fingerprint", "extensions",
		"dir_stats", "large_files", "characteristics"} {
		if _, exists := data[key]; !exists {
			t.Errorf("expected key %q in data", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Specific directory with files
// ---------------------------------------------------------------------------

func TestRun_SpecificDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\nworld\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &scanStructureTool{}
	args := makeArgs(map[string]any{"path": dir})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", tr.Status)
	}
	data := tr.Data.(map[string]any)

	if toFloat64(t, data["total_files"]) < 2 {
		t.Errorf("expected at least 2 files, got %v", data["total_files"])
	}
	if toFloat64(t, data["total_dirs"]) < 1 {
		t.Errorf("expected at least 1 dir, got %v", data["total_dirs"])
	}
	if toFloat64(t, data["total_lines"]) < 3 {
		t.Errorf("expected at least 3 lines, got %v", data["total_lines"])
	}

	// Extensions must be raw (".txt", ".go")
	exts := data["extensions"].(map[string]any)
	if _, ok := exts[".txt"]; !ok {
		t.Errorf("expected '.txt' in extensions")
	}
	if _, ok := exts[".go"]; !ok {
		t.Errorf("expected '.go' in extensions")
	}
}

// ---------------------------------------------------------------------------
// Go project — extensions + project metadata
// ---------------------------------------------------------------------------

func TestRun_GoProject(t *testing.T) {
	dir := t.TempDir()
	goMod := `module example.com/myapp

go 1.22
`
	mainGo := `package main

func main() {
	println("hello")
}
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &scanStructureTool{}
	args := makeArgs(map[string]any{"path": dir})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", tr.Status)
	}
	data := tr.Data.(map[string]any)

	// Raw extension stats
	exts := data["extensions"].(map[string]any)
	goStat, ok := exts[".go"].(map[string]any)
	if !ok {
		t.Fatal("expected '.go' in extensions")
	}
	if toFloat64(t, goStat["files"]) < 1 {
		t.Errorf("expected at least 1 .go file")
	}
	if toFloat64(t, goStat["lines"]) < 4 {
		t.Errorf("expected at least 4 .go lines")
	}

	// Project metadata from detectProject
	if data["project_type"] != "Go Module" {
		t.Errorf("expected project_type 'Go Module', got %v", data["project_type"])
	}
	if data["entry_point"] != "example.com/myapp" {
		t.Errorf("expected entry_point 'example.com/myapp', got %v", data["entry_point"])
	}
	if data["project_version"] != "1.22" {
		t.Errorf("expected project_version '1.22', got %v", data["project_version"])
	}

	// Fingerprint uses extension
	fp, ok := data["fingerprint"].(string)
	if !ok {
		t.Fatal("expected fingerprint string")
	}
	if !strings.Contains(fp, ".go") {
		t.Errorf("expected fingerprint to contain '.go', got %q", fp)
	}
	if !strings.Contains(fp, "go build") {
		t.Errorf("expected fingerprint to contain 'go build', got %q", fp)
	}
}

// ---------------------------------------------------------------------------
// Depth limit
// ---------------------------------------------------------------------------

func TestRun_DepthLimit(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c", "d")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "deep.txt"), []byte("deep\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("depth_1", func(t *testing.T) {
		tool := &scanStructureTool{}
		args := makeArgs(map[string]any{"path": dir, "depth": 1})
		result, err := tool.Run(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var tr toolutil.ToolResult
		if err := json.Unmarshal([]byte(result), &tr); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		if tr.Status != "ok" {
			t.Errorf("expected status 'ok', got %q", tr.Status)
		}
		data := tr.Data.(map[string]any)
		if toFloat64(t, data["total_files"]) != 0 {
			t.Errorf("expected 0 files with depth=1, got %v", data["total_files"])
		}
	})

	t.Run("depth_10", func(t *testing.T) {
		tool := &scanStructureTool{}
		args := makeArgs(map[string]any{"path": dir, "depth": 10})
		result, err := tool.Run(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var tr toolutil.ToolResult
		if err := json.Unmarshal([]byte(result), &tr); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		if tr.Status != "ok" {
			t.Errorf("expected status 'ok', got %q", tr.Status)
		}
		data := tr.Data.(map[string]any)
		if toFloat64(t, data["total_files"]) < 1 {
			t.Errorf("expected at least 1 file with depth=10, got %v", data["total_files"])
		}
	})
}

// ---------------------------------------------------------------------------
// Error cases
// ---------------------------------------------------------------------------

func TestRun_InvalidArgs(t *testing.T) {
	tool := &scanStructureTool{}
	_, err := tool.Run(context.Background(), `not json`)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "invalid args") {
		t.Errorf("expected error containing 'invalid args', got %v", err)
	}
}

func TestRun_NonExistentPath(t *testing.T) {
	tool := &scanStructureTool{}
	args := makeArgs(map[string]any{"path": filepath.Join(t.TempDir(), "nonexistent")})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tr toolutil.ToolResult
	if err := json.Unmarshal([]byte(result), &tr); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if tr.Status != "ok" {
		t.Errorf("expected status 'ok' for non-existent path (graceful fallback), got %q", tr.Status)
	}
}

func TestRun_FilePathError(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "somefile.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &scanStructureTool{}
	args := makeArgs(map[string]any{"path": filePath})
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Error("expected error when path is a file, not a directory")
	}
	if !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("expected error about not a directory, got %v", err)
	}
}

func TestRun_CancelledContext(t *testing.T) {
	tool := &scanStructureTool{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Run(ctx, `{}`)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

// ---------------------------------------------------------------------------
// Line counting + generated detection
// ---------------------------------------------------------------------------

func TestCountLinesAndCheckGenerated(t *testing.T) {
	dir := t.TempDir()

	normal := filepath.Join(dir, "normal.go")
	if err := os.WriteFile(normal, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	lines, gen, err := countLinesAndCheckGenerated(normal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lines != 3 {
		t.Errorf("expected 3 lines, got %d", lines)
	}
	if gen {
		t.Error("expected generated=false")
	}

	genFile := filepath.Join(dir, "gen.go")
	content := "// Code generated by protoc-gen-go. DO NOT EDIT.\npackage pb\n\nfunc init() {}\n"
	if err := os.WriteFile(genFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	lines, gen, err = countLinesAndCheckGenerated(genFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lines != 4 {
		t.Errorf("expected 4 lines, got %d", lines)
	}
	if !gen {
		t.Error("expected generated=true")
	}
}

// ---------------------------------------------------------------------------
// detectProject
// ---------------------------------------------------------------------------

func TestDetectProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info := detectProject(dir)
	if info.Type != "Go Module" {
		t.Errorf("expected 'Go Module', got %q", info.Type)
	}
	if info.Entry != "test" {
		t.Errorf("expected 'test', got %q", info.Entry)
	}
	if info.Version != "1.22" {
		t.Errorf("expected '1.22', got %q", info.Version)
	}
	if info.Build != "go build" {
		t.Errorf("expected build 'go build', got %q", info.Build)
	}
}

func TestDetectProjectUnknown(t *testing.T) {
	dir := t.TempDir()
	info := detectProject(dir)
	if info.Type != "Unknown" {
		t.Errorf("expected 'Unknown', got %q", info.Type)
	}
}

// ---------------------------------------------------------------------------
// buildFingerprint  (extension-based, no test split)
// ---------------------------------------------------------------------------

func TestBuildFingerprint(t *testing.T) {
	exts := map[string]langStat{
		".go":   {Files: 10, Lines: 500},
		".json": {Files: 2, Lines: 50},
	}
	chars := characteristics{
		HasMakefile:   true,
		HasDockerfile: true,
	}
	proj := projectInfo{Build: "go build"}

	fp := buildFingerprint(exts, chars, proj)
	// Expected: .go|go build|12 files|0KLOC|make,docker
	if !strings.Contains(fp, ".go") {
		t.Errorf("expected fingerprint to contain '.go', got %q", fp)
	}
	if !strings.Contains(fp, "go build") {
		t.Errorf("expected fingerprint to contain 'go build', got %q", fp)
	}
	if !strings.Contains(fp, "make") || !strings.Contains(fp, "docker") {
		t.Errorf("expected fingerprint to contain 'make' and 'docker', got %q", fp)
	}
}

func TestBuildFingerprint_Empty(t *testing.T) {
	fp := buildFingerprint(map[string]langStat{}, characteristics{}, projectInfo{})
	if fp == "" {
		t.Error("fingerprint should not be empty")
	}
}

// ---------------------------------------------------------------------------
// detectCharacteristics  (no HasMainPkg, no HasTests)
// ---------------------------------------------------------------------------

func TestDetectCharacteristics(t *testing.T) {
	dirs := map[string]bool{
		"cmd":      true,
		"api":      true,
		"docs":     true,
		".github":  true,
		"vendor":   true,
		"scripts":  true,
		"examples": true,
	}
	files := []fileInfo{
		{path: "Makefile", ext: ""},
		{path: "Dockerfile", ext: ""},
		{path: "service.proto", ext: ".proto"},
		{path: "generated.pb.go", ext: ".go", generated: true},
	}

	chars := detectCharacteristics(dirs, files)

	if !chars.HasMakefile {
		t.Error("expected HasMakefile")
	}
	if !chars.HasDockerfile {
		t.Error("expected HasDockerfile")
	}
	if !chars.HasCI {
		t.Error("expected HasCI (from .github dir)")
	}
	if !chars.HasCmdDir {
		t.Error("expected HasCmdDir")
	}
	if !chars.HasAPIDir {
		t.Error("expected HasAPIDir")
	}
	if !chars.HasDocs {
		t.Error("expected HasDocs")
	}
	if !chars.HasExamples {
		t.Error("expected HasExamples")
	}
	if !chars.HasProtobuf {
		t.Error("expected HasProtobuf")
	}
	if !chars.HasVendor {
		t.Error("expected HasVendor")
	}
	if !chars.HasGeneratedCode {
		t.Error("expected HasGeneratedCode")
	}
	if !chars.HasScripts {
		t.Error("expected HasScripts")
	}

}

// ---------------------------------------------------------------------------
// detectLargeFiles
// ---------------------------------------------------------------------------

func TestDetectLargeFiles(t *testing.T) {
	files := []fileInfo{
		{path: "small.go", ext: ".go", size: 100, lines: 10},
		{path: "big.go", ext: ".go", size: 600_000, lines: 2000},
		{path: "medium.go", ext: ".go", size: 100_000, lines: 1200},
	}
	large := detectLargeFiles(files)

	if len(large) != 2 {
		t.Fatalf("expected 2 large files, got %d: %+v", len(large), large)
	}
	if !strings.HasSuffix(large[0].Path, "big.go") {
		t.Errorf("expected big.go first (largest), got %s", large[0].Path)
	}
}

// ---------------------------------------------------------------------------
// buildDirStats
// ---------------------------------------------------------------------------

func TestBuildDirStats(t *testing.T) {
	files := []fileInfo{
		{dir: "pkg/api", lines: 300},
		{dir: "pkg/api", lines: 200},
		{dir: "internal", lines: 500},
		{dir: ".", lines: 50}, // skipped
		{dir: "cmd", lines: 100},
	}
	stats := buildDirStats(files)
	if len(stats) != 3 {
		t.Fatalf("expected 3 dir stats, got %d", len(stats))
	}

	got := map[string]int{}
	for _, s := range stats {
		got[s.Dir] = s.Lines
	}
	if got["internal"] != 500 {
		t.Errorf("expected internal 500 lines, got %d", got["internal"])
	}
	if got["pkg/api"] != 500 {
		t.Errorf("expected pkg/api 500 lines, got %d", got["pkg/api"])
	}
	if got["cmd"] != 100 {
		t.Errorf("expected cmd 100 lines, got %d", got["cmd"])
	}
	if stats[len(stats)-1].Dir != "cmd" {
		t.Errorf("expected cmd to be last (100 < 500), got %s", stats[len(stats)-1].Dir)
	}
}

// ---------------------------------------------------------------------------
// aggregateByExt  (raw extension grouping, no language names)
// ---------------------------------------------------------------------------

func TestAggregateByExt(t *testing.T) {
	files := []fileInfo{
		{ext: ".go", lines: 100},
		{ext: ".go", lines: 200},
		{ext: ".ts", lines: 50},
		{ext: ".go", lines: 0},
	}
	stats := aggregateByExt(files)

	goStat, ok := stats[".go"]
	if !ok {
		t.Fatal("expected '.go' key")
	}
	if goStat.Files != 3 {
		t.Errorf("expected 3 .go files, got %d", goStat.Files)
	}
	if goStat.Lines != 300 {
		t.Errorf("expected 300 .go lines, got %d", goStat.Lines)
	}

	tsStat, ok := stats[".ts"]
	if !ok {
		t.Fatal("expected '.ts' key")
	}
	if tsStat.Files != 1 {
		t.Errorf("expected 1 .ts file, got %d", tsStat.Files)
	}
}

func TestAggregateByExt_NoExt(t *testing.T) {
	files := []fileInfo{
		{ext: "Makefile", lines: 20},
		{ext: "Dockerfile", lines: 10},
	}
	// Files without a leading dot in ext will be grouped by raw ext string
	stats := aggregateByExt(files)
	total := 0
	for _, s := range stats {
		total += s.Files
	}
	if total != 2 {
		t.Errorf("expected 2 files total, got %d", total)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func toFloat64(t *testing.T, v any) float64 {
	t.Helper()
	switch val := v.(type) {
	case float64:
		return val
	case json.Number:
		f, err := val.Float64()
		if err != nil {
			t.Fatalf("cannot convert %v to float64", v)
		}
		return f
	default:
		t.Fatalf("unexpected type %T for value %v", v, v)
		return 0
	}
}
