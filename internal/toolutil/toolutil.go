// Package toolutil provides shared utility functions for gocel tools.
package toolutil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// IOWorkers returns the number of parallel I/O workers, clamped to [2, 16].
//
// IOWorkers 返回并行 I/O 工作协程数，范围限制在 [2, 16]。
func IOWorkers() int {
	n := runtime.NumCPU()
	if n < 2 {
		n = 2
	}
	if n > 16 {
		n = 16
	}
	return n
}

// IsSkippedDir reports whether name is a directory that should be skipped
// during traversal (e.g. .git, node_modules, vendor).
//
// IsSkippedDir 判断 name 是否属于遍历时应跳过的目录（如 .git、
// node_modules、vendor 等）。
func IsSkippedDir(name string) bool {
	skipped := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		"__pycache__": true, ".tox": true, "target": true,
		"build": true, "dist": true,
	}
	return skipped[name]
}

// IsTextFile reports whether the file's extension is not a known binary
// extension.
//
// IsTextFile 判断文件扩展名是否不属于已知的二进制扩展名。
func IsTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	binaryExt := map[string]bool{
		".exe": true, ".dll": true, ".so": true,
		".o": true, ".a": true, ".obj": true, ".class": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".mp3": true, ".mp4": true, ".zip": true, ".tar": true,
		".gz": true, ".pdf": true, ".ttf": true,
	}
	return !binaryExt[ext]
}

// IsBinaryFile reports whether the file is binary: it has a binary extension
// or contains null bytes in its first 8KB.
//
// IsBinaryFile 判断文件是否为二进制文件：扩展名为二进制类型，或前 8KB
// 内容包含空字节。
func IsBinaryFile(path string) bool {
	if !IsTextFile(path) {
		return true
	}
	// Quick content check: look for null bytes in first 8KB
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	return false
}

// IsTextExt reports whether ext (a file extension including the dot) is a
// text extension.
//
// IsTextExt 判断 ext（含点号的扩展名）是否为文本扩展名。
func IsTextExt(ext string) bool {
	return !map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".o": true, ".a": true, ".obj": true, ".lib": true, ".class": true,
		".bin": true, ".dat": true, ".pyc": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".ico": true, ".webp": true,
		".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true,
		".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".7z": true, ".rar": true,
		".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
		".ttf": true, ".otf": true, ".woff": true, ".woff2": true,
	}[ext]
}

// TypeExts returns the set of file extensions associated with the given
// language type name (e.g. "go", "python"), falling back to "."+typeName for
// unknown types.
//
// TypeExts 返回给定语言类型名（如 "go"、"python"）关联的扩展名集合；
// 未知类型回退为 "."+typeName。
func TypeExts(typeName string) map[string]bool {
	extMap := map[string][]string{
		"go": {".go"}, "rust": {".rs"}, "rs": {".rs"},
		"python": {".py", ".pyi"}, "py": {".py", ".pyi"},
		"js": {".js", ".mjs"}, "ts": {".ts", ".tsx"},
		"java": {".java"}, "c": {".c", ".h"},
		"ruby": {".rb"}, "shell": {".sh", ".bash"},
		"yaml": {".yaml", ".yml"}, "json": {".json"},
		"markdown": {".md"}, "md": {".md"},
		"html": {".html"}, "css": {".css", ".scss"},
	}
	exts, ok := extMap[strings.ToLower(typeName)]
	if !ok {
		return map[string]bool{"." + strings.ToLower(typeName): true}
	}
	result := make(map[string]bool, len(exts))
	for _, e := range exts {
		result[e] = true
	}
	return result
}

// DefaultPerm returns the default file permission mode (0644).
//
// DefaultPerm 返回默认的文件权限模式（0644）。
func DefaultPerm() os.FileMode { return 0644 }

// FormatResult marshals a successful tool result (status "ok") to JSON.
//
// FormatResult 将成功的工具结果（状态 "ok"）序列化为 JSON 字符串。
func FormatResult(msg string, data map[string]any) string {
	tr := ToolResult{Status: "ok", Message: msg, Data: data}
	b, _ := json.Marshal(tr)
	return string(b)
}

// FormatError marshals an error tool result (status "error") to JSON.
//
// FormatError 将错误工具结果（状态 "error"）序列化为 JSON 字符串。
func FormatError(err error) string {
	tr := ToolResult{Status: "error", Message: err.Error(), Error: err.Error()}
	b, _ := json.Marshal(tr)
	return string(b)
}

// ToolResult is the standard response wrapper for tool results in tests.
//
// ToolResult 是测试中工具结果的标准响应包装结构。
type ToolResult struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GlobMatch reports whether path matches pattern using segment-level glob
// semantics, where "**" spans zero or more segments.
//
// GlobMatch 按段（segment）级通配语义判断 path 是否匹配 pattern，
// 其中 "**" 匹配零个或多个段。
func GlobMatch(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	// Trailing slashes are meaningless for matching ("foo/" == "foo").
	pattern = strings.TrimSuffix(pattern, "/")
	path = strings.TrimSuffix(path, "/")
	if pattern == "" {
		return path == ""
	}
	// Segment-level matching for ALL patterns: a single '*' must never
	// cross a path separator (classic glob semantics — Go's filepath.Match
	// is more permissive on Windows, where '/' inside names leaks across
	// pattern segments).
	pparts := strings.Split(pattern, "/")
	fparts := strings.Split(path, "/")
	return matchSegments(pparts, fparts, 0, 0, map[[2]int]bool{})
}

// matchSegments matches pattern segments against path segments; "**" spans
// zero or more segments, every other segment uses filepath.Match.
func matchSegments(pat, path []string, pi, si int, memo map[[2]int]bool) bool {
	key := [2]int{pi, si}
	if v, ok := memo[key]; ok {
		return v
	}
	res := func(v bool) bool {
		memo[key] = v
		return v
	}
	if pi == len(pat) {
		return res(si == len(path))
	}
	seg := pat[pi]
	if seg == "**" {
		// Zero segments, then one or more (recursion consumes them).
		if matchSegments(pat, path, pi+1, si, memo) {
			return res(true)
		}
		if si < len(path) {
			return res(matchSegments(pat, path, pi, si+1, memo))
		}
		return res(false)
	}
	if si >= len(path) {
		return res(false)
	}
	ok, _ := filepath.Match(seg, path[si])
	if !ok {
		return res(false)
	}
	return res(matchSegments(pat, path, pi+1, si+1, memo))
}

// ── File version guard（文件版本守卫）────────────────────────────────────
//
// read-before-edit 的机制基座（DSH fs-observation-policy 语义）：read
// 返回文件的版本指纹，write/multiedit 在修改前校验指纹——文件在读取后
// 被外部改动时拒绝写入（FS_STALE_VERSION），防止模型盲改陈旧内容。
// 指纹只用于新鲜度判断，不是权限或身份边界。

// FileVersion returns a stable fingerprint of the file's current content
// identity: size:mtimeNs:mode. The size and modification time changing is
// a reliable freshness signal for the read-before-edit guard — the
// fingerprint is a heuristic, not a checksum.
//
// FileVersion 返回文件当前内容身份的稳定指纹：size:mtimeNs:mode。
// 大小与修改时间变化是 read-before-edit 守卫的可靠新鲜度信号——
// 指纹是启发式，不是校验和。
func FileVersion(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("version: stat %q: %w", path, err)
	}
	return strconv.FormatInt(info.Size(), 16) + ":" +
		strconv.FormatInt(info.ModTime().UnixNano(), 16) + ":" +
		strconv.FormatUint(uint64(info.Mode().Perm()), 16), nil
}

// CheckVersion verifies that the file's current fingerprint matches the
// expected one captured by an earlier read. A mismatch means the file
// changed since the read — the edit must be rejected, never applied blind.
//
// CheckVersion 校验文件当前指纹与先前 read 捕获的期望指纹一致。不一致
// 说明文件在读取后发生了变化——编辑必须被拒绝，绝不盲改。
func CheckVersion(path, expected string) error {
	if expected == "" {
		return nil // no expectation = unguarded write (legacy behavior)
	}
	cur, err := FileVersion(path)
	if err != nil {
		return err
	}
	if cur != expected {
		return &StaleVersionError{Path: path, Expected: expected, Current: cur}
	}
	return nil
}

// StaleVersionError is the closed stale-version vocabulary (DSH
// FS_STALE_VERSION): the file changed after the caller's last read.
//
// StaleVersionError 是陈旧版本的封闭错误词汇（DSH FS_STALE_VERSION）：
// 文件在调用方上次读取后发生了变化。
type StaleVersionError struct {
	Path     string
	Expected string
	Current  string
}

// Error implements error.
//
// Error 实现 error 接口。
func (e *StaleVersionError) Error() string {
	return fmt.Sprintf("file %q changed since it was read (stale version) — re-read the file, then retry", e.Path)
}

// IsStaleVersion reports whether err is (or wraps) a StaleVersionError.
//
// IsStaleVersion 判断 err 是否为（或包装了）StaleVersionError。
func IsStaleVersion(err error) bool {
	_, ok := err.(*StaleVersionError)
	return ok
}
