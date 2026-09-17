// Package jsonx provides fault-tolerant JSON parsing for LLM output — the
// strategy layer's single source of truth, living in the changeable layer
// (gocel) because repair rules must evolve with model capabilities. The
// core (gocel/core) stays strict by contract; consumers opt into jsonx
// where LLM output enters.
//
// LLM output is naturally full of minor syntax defects: full-width
// punctuation, trailing commas, raw newlines inside strings, single-quoted
// strings, bare keys, and prose or code fences around the JSON value.
// Strict parsing would fail an entire agent phase over such noise.
// jsonx repairs syntax only — it never infers semantics. Field names and
// types remain the caller's contract to validate.
//
// Use jsonx.Unmarshal for model-generated tool arguments (JSON expected,
// defects tolerated) and jsonx.Parse for model text output (JSON embedded
// in prose/code fences). Protocol boundaries (A2A, MCP, provider SSE) and
// harness-persisted state must stay strict — jsonx is not for them.
//
// jsonx 提供模型输出的 JSON 容错解析——策略层的单一来源，位于变化
// 扩展层（gocel）：修复规则必须随模型能力演进，不能冻结进核心。
// 核心（gocel/core）按契约保持严格解析；消费方在模型输出进入处按需
// 选择 jsonx。
//
// LLM 输出天然带语法瑕疵（全角标点、尾随逗号、字符串内裸换行、
// 单引号字符串、裸键、JSON 前后缀文本与代码块包裹）。严格解析会因
// 这些噪音报废整个 agent 阶段。jsonx 只做语法级容错（不改变语义），
// 字段名与类型仍由调用方契约校验。
//
// 工具参数（JSON 文本，容忍瑕疵）用 jsonx.Unmarshal；模型正文输出
// （JSON 嵌在散文/代码块中）用 jsonx.Parse。协议边界（A2A、MCP、
// provider SSE）与 harness 自身落盘数据必须保持严格解析——那不是
// jsonx 的适用场景。
package jsonx

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Parse parses a JSON value embedded in model text output: extract →
// repair → strict decode. Tolerates ```json code fences, surrounding
// prose (including braces inside prose strings), and the syntax defects
// listed in the package doc. v must be a non-nil pointer.
//
// Parse 从模型文本输出中解析 JSON 值：截取 → 语法修复 → 严格解构。
// 容忍 ```json 代码块、前后缀文本（含散文中的花括号）与包文档列出的
// 语法瑕疵。v 必须是非 nil 指针。
func Parse(content string, v any) error {
	if v == nil {
		return fmt.Errorf("jsonx: target is nil")
	}
	value, err := extractValue(content)
	if err != nil {
		return err
	}
	return unmarshalRepaired([]byte(value), v)
}

// Unmarshal decodes model-generated tool arguments (or any JSON text with
// minor syntax defects). It repairs syntax then decodes strictly; it does
// not extract from prose. v must be a non-nil pointer.
//
// Unmarshal 解码模型生成的工具参数（或带轻微语法瑕疵的 JSON 文本）。
// 先修复语法再严格解构；不做文本截取。v 必须是非 nil 指针。
func Unmarshal(data []byte, v any) error {
	if v == nil {
		return fmt.Errorf("jsonx: target is nil")
	}
	return unmarshalRepaired(data, v)
}

// RepairString repairs syntax defects in a JSON text and returns the
// repaired text. It errors if the content cannot be repaired into valid
// JSON. The engine loop (StepLoop) uses it to normalize model-generated
// tool arguments before they reach guard hooks and tool execution.
//
// RepairString 修复 JSON 文本的语法瑕疵并返回修复后的文本；内容无法
// 修复为合法 JSON 时返回错误。StepLoop 用它归一化模型生成的工具参数，
// 使守卫钩子与工具执行收到可解析的参数。
func RepairString(s string) (string, error) {
	repaired, err := repairValid(s)
	if err != nil {
		return "", err
	}
	return repaired, nil
}

func repairValid(s string) (string, error) {
	repaired := repair(s)
	var v any
	if err := json.Unmarshal([]byte(repaired), &v); err != nil {
		return "", fmt.Errorf("jsonx: %w", err)
	}
	return repaired, nil
}

func unmarshalRepaired(data []byte, v any) error {
	repaired, err := repairValid(string(data))
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(repaired), v); err != nil {
		return fmt.Errorf("jsonx: %w", err)
	}
	return nil
}

// LenientBool accepts JSON booleans (true/false) and their string forms
// ("true"/"false"/"1"/"0", case-insensitive, with or without quotes).
// Models frequently emit booleans as strings; strict typing would fail
// the whole phase parse. An empty string is treated as undeclared
// (conservatively false).
//
// LenientBool 宽松布尔：接受 JSON 布尔（true/false）与字符串形式
// （"true"/"false"/"1"/"0"，忽略大小写与引号）。模型常把布尔输出成
// 字符串，严格类型会让整个阶段解析失败。空串视为未声明（保守取 false）。
type LenientBool bool

// UnmarshalJSON implements lenient boolean parsing.
// UnmarshalJSON 实现宽松布尔解析。
func (b *LenientBool) UnmarshalJSON(data []byte) error {
	// Trim whitespace outside the quotes first, strip the quotes, then
	// trim again — `" true "` (whitespace inside quotes) must parse.
	raw := strings.TrimSpace(string(data))
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
	}
	switch s := strings.ToLower(strings.TrimSpace(raw)); s {
	case "true", "1":
		*b = true
	case "false", "0", "": // 空串视为未声明（保守取 false）
		*b = false
	default:
		return fmt.Errorf("jsonx: 无法解析布尔值 %q", string(data))
	}
	return nil
}

// ── 截取 ────────────────────────────────────────────────────────────────

// extractValue 从内容中截取 JSON 值（对象或数组）的候选片段，返回第一个
// 能解析的。内容本身是 JSON 时直接返回整体。
func extractValue(content string) (string, error) {
	s := strings.TrimSpace(content)
	if s == "" {
		return "", fmt.Errorf("jsonx: 空内容")
	}
	// 常见情况：内容本身就是 JSON 值，无需扫描候选。
	if isJSON(s) {
		return s, nil
	}
	// 逐候选尝试：散文/代码块中的 {、[ 位置（不在字符串内）。
	for _, start := range valueStarts(s) {
		end := valueEnd(s, start)
		if end < 0 {
			continue
		}
		cand := s[start : end+1]
		if isJSON(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("jsonx: 内容中未找到可解析的 JSON 值")
}

// isJSON 语法级验证：修复后能否解析（不校验结构，仅验证语法）。
func isJSON(s string) bool {
	var v any
	return json.Unmarshal([]byte(repair(s)), &v) == nil
}

// valueStarts 返回不在字符串内的 { / [ 位置（候选起点），最多 32 个。
// 字符串规则与 repair 保持一致（双引号+转义、全角配对、单引号+撇号）。
func valueStarts(s string) []int {
	starts := make([]int, 0, 4)
	sc := &strScan{inDQ: false, esc: false, fw: 0, sq: 0}
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !sc.inside() {
			switch r {
			case '{', '[':
				starts = append(starts, i)
				if len(starts) >= 32 {
					return starts
				}
			}
		}
		sc.step(r, s, i)
		i += size
	}
	return starts
}

// valueEnd 从 start 处做平衡扫描，返回深度归零的结束位置；未闭合返回 -1。
// 字符串规则与 repair 保持一致。
func valueEnd(s string, start int) int {
	depth := 0
	sc := &strScan{inDQ: false, esc: false, fw: 0, sq: 0}
	i := start
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !sc.inside() {
			switch r {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i
				}
			}
		}
		sc.step(r, s, i)
		i += size
	}
	return -1
}

// strScan 跟踪 JSON 字符串状态：双引号（含转义）、全角引号配对、
// 单引号（含撇号启发式）。valueStarts/valueEnd/repair 共用同一规则，
// 修改必须三处同步。
type strScan struct {
	inDQ bool // 双引号字符串内
	esc  bool // 当前字符被转义
	fw   int  // 全角引号配对深度（>0 表示在“...”内）
	sq   int  // 单引号配对深度（>0 表示在 '...' 或 ‘...’ 内）
}

// inside 当前是否处于任一字符串内。
func (sc *strScan) inside() bool { return sc.inDQ || sc.fw > 0 || sc.sq > 0 }

// step 推进一个 rune 并更新字符串状态。s 与 i 提供上下文（撇号判定）。
func (sc *strScan) step(r rune, s string, i int) {
	switch {
	case sc.inDQ:
		switch {
		case sc.esc:
			sc.esc = false
		case r == '\\':
			sc.esc = true
		case r == '"':
			sc.inDQ = false
		}
	case sc.fw > 0:
		switch {
		case r == '”':
			sc.fw--
		case r == '“':
			sc.fw++
		}
	case sc.sq > 0:
		switch {
		case r == '\'' && !sqCloses(s, i):
			// 撇号，留在串内
		case r == '\'':
			sc.sq--
		case r == '’':
			sc.sq--
		case r == '‘':
			sc.sq++
		}
	default: // 结构位置
		switch {
		case r == '"':
			sc.inDQ = true
		case r == '“':
			// 只有左引号开启全角字符串；孤立的右引号（” ）是噪音。
			sc.fw = 1
		case r == '‘':
			// 只有左引号开启全角单引号字符串；孤立的右引号（’）是噪音。
			sc.sq = 1
		case r == '\'' && !apostrophe(s, i):
			sc.sq = 1
		}
	}
}

// apostrophe 判定结构位置的 ' 是否为撇号（前一个非空白字符是单词字符，
// 如 don't —— 撇号不开启字符串）。
func apostrophe(s string, pos int) bool {
	j := pos - 1
	for j >= 0 && isSpaceByte(s[j]) {
		j--
	}
	if j < 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:j+1])
	return isWord(r)
}

// sqCloses 判定单引号字符串内的 ' 是否闭合字符串（后一个非空白字符
// 是结构符时闭合；后跟单词字符是撇号，如 'it's fine'）。
func sqCloses(s string, pos int) bool {
	j := pos + 1
	for j < len(s) && isSpaceByte(s[j]) {
		j++
	}
	if j >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[j:])
	return !isWord(r)
}

// ── 语法修复 ────────────────────────────────────────────────────────────

// repair 单遍修复常见语法瑕疵：
//   - 全角引号 “ ” ‘ ’ → 半角（字符串内容保留原文，配对深度处理嵌套）；
//   - 结构位置的全角冒号/逗号 → 半角；尾随逗号剥离（跳过字符串字面量）；
//   - 字符串内裸换行/制表符/控制字符 → 转义序列；
//   - 单引号字符串 → 双引号（撇号启发式，不误伤 don't 之类内容）；
//   - 键位置裸标识符（{a: 1}）→ 加引号。
func repair(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	sc := &strScan{inDQ: false, esc: false, fw: 0, sq: 0}
	prev := rune(0) // 结构位置的上一个非空白字符（裸键判定）
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case sc.inDQ:
			repairInDQ(&b, sc, r)
		case sc.fw > 0:
			repairInFW(&b, sc, r)
		case sc.sq > 0:
			repairInSQ(&b, sc, s, i, r)
		default: // 结构位置
			switch {
			case r == '"':
				b.WriteRune(r)
				sc.inDQ = true
			case r == '“':
				b.WriteRune('"')
				sc.fw = 1
			case r == '‘':
				b.WriteRune('"')
				sc.sq = 1
			case r == '”' || r == '’':
				// 结构位置的孤立右引号：噪音，丢弃（不开启字符串，
				// 否则会吞掉后续 JSON 值）。
			case r == '\'' && !apostrophe(s, i):
				b.WriteRune('"')
				sc.sq = 1
			case r == '：':
				b.WriteRune(':')
			case r == ',' || r == '，':
				// 尾随逗号：其后（跳过空白）紧跟 } 或 ] 时丢弃。
				if trailingComma(s, i+size) {
					// 丢弃
				} else {
					b.WriteRune(',')
				}
			case isWord(r) && keyPosition(prev) && bareKeyColon(s, i):
				// 裸键 {a: 1} → 加引号。continue 会跳过循环尾的 i += size，
				// 所以这里直接把 i 推进到键名结束位置。
				b.WriteRune('"')
				for j := i; j < len(s); {
					r2, size2 := utf8.DecodeRuneInString(s[j:])
					if !isWord(r2) {
						break
					}
					b.WriteRune(r2)
					j += size2
					i = j
				}
				b.WriteRune('"')
				continue
			default:
				b.WriteRune(r)
			}
			if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
				prev = r
			}
		}
		i += size
	}
	return b.String()
}

// repairInDQ handles a rune inside a double-quoted string: control
// characters become escapes.
func repairInDQ(b *strings.Builder, sc *strScan, r rune) {
	switch {
	case sc.esc:
		b.WriteRune(r)
		sc.esc = false
	case r == '\\':
		b.WriteRune(r)
		sc.esc = true
	case r == '"':
		b.WriteRune(r)
		sc.inDQ = false
	case r == '\n':
		b.WriteString(`\n`)
	case r == '\r':
		b.WriteString(`\r`)
	case r == '\t':
		b.WriteString(`\t`)
	case r < 0x20:
		fmt.Fprintf(b, `\u%04x`, r)
	default:
		b.WriteRune(r)
	}
}

// repairInFW handles a rune inside a full-width-quoted string.
func repairInFW(b *strings.Builder, sc *strScan, r rune) {
	switch {
	case r == '”':
		sc.fw--
		if sc.fw == 0 {
			b.WriteRune('"') // 闭合符 → 半角引号
		} else {
			b.WriteRune('”') // 嵌套配对是内容
		}
	case r == '“':
		b.WriteRune('“') // 嵌套配对是内容
		sc.fw++
	case r == '\n':
		b.WriteString(`\n`)
	case r == '\r':
		b.WriteString(`\r`)
	case r == '\t':
		b.WriteString(`\t`)
	default:
		b.WriteRune(r)
	}
}

// repairInSQ handles a rune inside a single-quoted string: closing quotes
// become double quotes, apostrophes stay content.
func repairInSQ(b *strings.Builder, sc *strScan, s string, i int, r rune) {
	switch {
	case r == '\'' && !sqCloses(s, i):
		b.WriteRune('\'') // 撇号是内容
	case r == '\'':
		sc.sq--
		b.WriteRune('"') // 闭合符 → 半角引号
	case r == '’':
		sc.sq--
		if sc.sq == 0 {
			b.WriteRune('"')
		} else {
			b.WriteRune('’')
		}
	case r == '‘':
		b.WriteRune('‘')
		sc.sq++
	case r == '\n':
		b.WriteString(`\n`)
	case r == '\r':
		b.WriteString(`\r`)
	case r == '\t':
		b.WriteString(`\t`)
	default:
		b.WriteRune(r)
	}
}

// trailingComma 判定逗号（位置 pos）之后是否紧跟结构闭合符（剥离尾随逗号）。
func trailingComma(s string, pos int) bool {
	j := pos
	for j < len(s) && isSpaceByte(s[j]) {
		j++
	}
	return j < len(s) && (s[j] == '}' || s[j] == ']')
}

// keyPosition 判定上一个结构字符是否构成键位置（{ 或 , 之后）。
func keyPosition(prev rune) bool { return prev == '{' || prev == ',' }

// bareKeyColon 判定 s[i:] 处的标识符之后（跳过空白）是否紧跟冒号。
func bareKeyColon(s string, i int) bool {
	j := i
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if !isWord(r) {
			break
		}
		j += size
	}
	for j < len(s) && isSpaceByte(s[j]) {
		j++
	}
	if j >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[j:])
	return r == ':' || r == '：'
}

func isWord(r rune) bool {
	return r == '_' || r == '$' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
