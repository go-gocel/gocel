package jsonx

import (
	"strings"
	"testing"
)

type obj struct {
	A     int         `json:"a"`
	B     int         `json:"b"`
	Desc  string      `json:"desc"`
	Done  LenientBool `json:"done"`
	Cover []string    `json:"cover"`
	Items []string    `json:"items"`
	Notes string      `json:"notes"`
	Flag  LenientBool `json:"flag"`
}

func TestParse_PlainJSON(t *testing.T) {
	var o obj
	if err := Parse(`{"a":1,"desc":"x"}`, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if o.A != 1 || o.Desc != "x" {
		t.Fatalf("got %+v", o)
	}
}

func TestParse_FullWidthPunctuation(t *testing.T) {
	// 全角引号/冒号/逗号 + 尾随逗号（迁移自 gocode 既有容错用例）。
	var o obj
	in := `{“a”: 1，“b”: 2，}`
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if o.A != 1 || o.B != 2 {
		t.Fatalf("got %+v", o)
	}
}

func TestParse_FullWidthQuotedStringKeepsContent(t *testing.T) {
	// 字符串内部的全角引号/全角逗号是合法内容，不得被误改；
	// 只有结构位置的尾随逗号应被剥离。
	var o obj
	in := `{"desc":"他说“好”，然后",}`
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := "他说“好”，然后"; o.Desc != want {
		t.Fatalf("desc = %q, want %q", o.Desc, want)
	}
}

func TestParse_FullWidthNestedQuotes(t *testing.T) {
	var o obj
	in := `{“desc”: “他说“好”然后”, “a”: 1}`
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := "他说“好”然后"; o.Desc != want {
		t.Fatalf("desc = %q, want %q", o.Desc, want)
	}
	if o.A != 1 {
		t.Fatalf("a = %d", o.A)
	}
}

func TestParse_RawNewlineInString(t *testing.T) {
	// 模型常把多行内容直接写成裸换行——JSON 语法非法，必须转义。
	var o obj
	in := "{\"notes\":\"第一行\n第二行\",\"a\":1}"
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := "第一行\n第二行"; o.Notes != want {
		t.Fatalf("notes = %q, want %q", o.Notes, want)
	}
	if o.A != 1 {
		t.Fatalf("a = %d", o.A)
	}
}

func TestParse_RawTabInString(t *testing.T) {
	var o obj
	in := "{\"notes\":\"a\tb\"}"
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if o.Notes != "a\tb" {
		t.Fatalf("notes = %q", o.Notes)
	}
}

func TestParse_SingleQuotedStrings(t *testing.T) {
	// 单引号字符串 → 双引号；撇号（it's）不得误伤。
	var o obj
	in := `{'desc': 'it's fine', 'a': 1}`
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if o.Desc != "it's fine" || o.A != 1 {
		t.Fatalf("got %+v", o)
	}
}

func TestParse_BareKeys(t *testing.T) {
	in := `{a: 1, b: [true, false]}` // b 是数组，解析到任意字段前先验证语法
	var v map[string]any
	if err := Parse(in, &v); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if v["a"] != float64(1) {
		t.Fatalf("a = %v", v["a"])
	}
	arr, ok := v["b"].([]any)
	if !ok || len(arr) != 2 || arr[0] != true || arr[1] != false {
		t.Fatalf("b = %v", v["b"])
	}
}

func TestParse_CodeFence(t *testing.T) {
	var o obj
	in := "```json\n{\"a\": 1, \"desc\": \"x\"}\n```"
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if o.A != 1 || o.Desc != "x" {
		t.Fatalf("got %+v", o)
	}
}

func TestParse_ProseAround(t *testing.T) {
	var o obj
	in := "排查结果如下：\n{\"a\": 1, \"desc\": \"x\"}\n以上。"
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if o.A != 1 || o.Desc != "x" {
		t.Fatalf("got %+v", o)
	}
}

func TestParse_ProseWithBraces(t *testing.T) {
	// 散文里的花括号不得干扰截取；字符串里的 } 不得提前闭合。
	var o obj
	in := `结果 {示例} 如下：{"desc": "a}b", "a": 1}`
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if o.Desc != "a}b" || o.A != 1 {
		t.Fatalf("got %+v", o)
	}
}

func TestParse_ArrayValue(t *testing.T) {
	var v []map[string]any
	in := `[{"a": 1}, {"b": 2}]`
	if err := Parse(in, &v); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(v) != 2 || v[0]["a"] != float64(1) {
		t.Fatalf("got %v", v)
	}
}

func TestParse_ArrayWithProse(t *testing.T) {
	var v []map[string]any
	in := "结果：```json\n[{\"a\": 1}]\n``` 完。"
	if err := Parse(in, &v); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(v) != 1 || v[0]["a"] != float64(1) {
		t.Fatalf("got %v", v)
	}
}

func TestParse_StringBool(t *testing.T) {
	var o obj
	in := `{"done": "true", "flag": "0"}`
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !o.Done {
		t.Fatal("done should be true")
	}
	if o.Flag {
		t.Fatal("flag should be false")
	}
}

func TestParse_Errors(t *testing.T) {
	var o obj
	for _, in := range []string{"", "   ", "not json at all", "结果如下：完成", "{\"a\": 1" /*缺右括号*/} {
		if err := Parse(in, &o); err == nil {
			t.Errorf("Parse(%q) should fail", in)
		}
	}
	if err := Parse(`{"a":1}`, nil); err == nil {
		t.Error("nil target should fail")
	}
}

func TestUnmarshal_ToolArgs(t *testing.T) {
	// 工具参数：轻微瑕疵（尾随逗号、字符串布尔、全角引号）应容错。
	var args struct {
		Command string      `json:"command"`
		Force   LenientBool `json:"force"`
	}
	in := `{"command": "systemctl restart nginx", "force": "true",}`
	if err := Unmarshal([]byte(in), &args); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if args.Command != "systemctl restart nginx" || !args.Force {
		t.Fatalf("got %+v", args)
	}
}

func TestUnmarshal_StrictShape(t *testing.T) {
	// Unmarshal 不做文本截取：带散文的内容应失败（那是 Parse 的职责）。
	var o obj
	if err := Unmarshal([]byte(`结果：{"a":1}`), &o); err == nil {
		t.Fatal("Unmarshal with prose should fail")
	}
}

func TestUnmarshal_RepairsRawNewline(t *testing.T) {
	var args struct {
		Command string `json:"command"`
	}
	in := "{\"command\": \"echo hi\nwhoami\",}"
	if err := Unmarshal([]byte(in), &args); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if want := "echo hi\nwhoami"; args.Command != want {
		t.Fatalf("command = %q, want %q", args.Command, want)
	}
}

func TestLenientBool(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{`true`, true}, {`false`, false},
		{`"true"`, true}, {`"false"`, false},
		{`"TRUE"`, true}, {`"False"`, false},
		{`"1"`, true}, {`"0"`, false},
		{`1`, true}, {`0`, false},
		{`""`, false}, // 空串视为未声明
	} {
		var b LenientBool
		if err := b.UnmarshalJSON([]byte(tc.in)); err != nil {
			t.Errorf("UnmarshalJSON(%q): %v", tc.in, err)
			continue
		}
		if bool(b) != tc.want {
			t.Errorf("UnmarshalJSON(%q) = %v, want %v", tc.in, b, tc.want)
		}
	}
	if err := new(LenientBool).UnmarshalJSON([]byte(`"maybe"`)); err == nil {
		t.Error("unparseable bool should fail")
	}
}

func TestParse_KeepsEscapedSequences(t *testing.T) {
	// 已转义的内容不得被二次修复破坏。
	var o obj
	in := `{"notes": "line1\nline2\t\"quoted\"", "a": 1}`
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := "line1\nline2\t\"quoted\""; o.Notes != want {
		t.Fatalf("notes = %q, want %q", o.Notes, want)
	}
}

func TestParse_ArrayOfStrings(t *testing.T) {
	var v []string
	in := `["a", "b"]`
	if err := Parse(in, &v); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(v) != 2 || v[0] != "a" || v[1] != "b" {
		t.Fatalf("got %v", v)
	}
}

func TestParse_ProseApostrophe(t *testing.T) {
	// 散文中的撇号不得干扰后续 JSON 的截取。
	var o obj
	in := `don't worry: {"a": 1}`
	if err := Parse(in, &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if o.A != 1 {
		t.Fatalf("got %+v", o)
	}
}

func TestParse_LongContent(t *testing.T) {
	// 多字段阶段小结全量路径（贴近 gocode 真实输出形态）。
	var o obj
	var sb strings.Builder
	sb.WriteString("阶段小结：\n```json\n")
	sb.WriteString(`{"done": "true", "a": 1, "desc": "服务异常", `)
	sb.WriteString(`"cover": ["cpu", "mem", "disk"], `)
	sb.WriteString(`"notes": "第一行` + "\n" + `第二行",}`)
	sb.WriteString("\n```\n以上。")
	if err := Parse(sb.String(), &o); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !o.Done || o.A != 1 || len(o.Cover) != 3 {
		t.Fatalf("got %+v", o)
	}
	if o.Notes != "第一行\n第二行" {
		t.Fatalf("notes = %q", o.Notes)
	}
}
