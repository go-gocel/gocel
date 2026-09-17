// Package tool provides tool definition, MCP source, skill loading, and registry.
//
// Tools are the primary extension point for agents. Three injection methods:
//   - FuncTool: define tools from Go functions
//   - MCPSource: discover tools from MCP servers
//   - LoadSkills: load tools from skill file directories
//
// Registry provides MapToolRegistry for name-based tool lookup.
//
// 工具系统包。提供 FuncTool（函数定义）、MCPSource（MCP 服务发现）、
// LoadSkills（技能目录加载）三种工具注入方式，以及 MapToolRegistry 注册表。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
)

// FuncTool 基于 Go 函数构造的工具，自动推导 JSON Schema。
//
// FuncTool 的核心能力是通过反射自动解析任意 Go 函数签名的参数列表，
// 生成符合 LLM Tool/Function Calling 协议的 JSON Schema。
// 支持普通参数、struct 参数、variadic 参数、context.Context 第一参数约定。
// 返回值支持返回 single value、string + error、或纯 error。
//
// FuncTool is a tool constructed from Go functions with automatic JSON Schema inference.
//
//	add := tool.ToolFromFunc(func(ctx context.Context, a, b int) int { return a + b })
//	add.Name()       // "func1" (auto-detected)
//	add.Description() // "" (auto)
//	add.Run(ctx, `{"a":1,"b":2}`) // "3"
type FuncTool struct {
	name        string
	description string
	fn          any

	fnValue      reflect.Value
	fnType       reflect.Type
	argSpecs     []argSpec
	hasResult    bool
	resultIsStr  bool
	singleStruct bool

	schema     map[string]any
	schemaOnce sync.Once

	kind    kernel.ToolKind
	source  string
	tags    []string
	effects []kernel.ToolEffect
	timeout int64
}

type argSpec struct {
	Name        string
	Type        reflect.Type
	IsStruct    bool
	Required    bool
	Description string
}

// FuncToolOption configures a FuncTool created via ToolFromFunc / MustToolFromFunc.
//
// FuncToolOption 是通过 ToolFromFunc / MustToolFromFunc 创建 FuncTool 时的配置选项函数。
type FuncToolOption func(*funcToolCfg)

type funcToolCfg struct {
	name        string
	description string
	argNames    []string
	argDescs    []string
	kind        kernel.ToolKind
	source      string
	tags        []string
	effects     []kernel.ToolEffect
	timeout     int64
}

// WithToolName sets the tool's name. If not set, the function name is used.
//
// WithToolName 设置工具名称。未设置时使用函数名称。
func WithToolName(name string) FuncToolOption {
	return func(c *funcToolCfg) { c.name = name }
}

// WithToolDescription sets the tool's description that will be sent to the LLM.
//
// WithToolDescription 设置工具描述，该描述会发送给 LLM 用于理解工具用途。
func WithToolDescription(desc string) FuncToolOption {
	return func(c *funcToolCfg) { c.description = desc }
}

// WithArgNames sets custom argument names for the tool schema.
//
// WithArgNames 设置工具参数的自定义名称。
func WithArgNames(names ...string) FuncToolOption {
	return func(c *funcToolCfg) { c.argNames = names }
}

// WithArgDescs sets custom argument descriptions for the tool schema.
//
// WithArgDescs 设置工具参数的自定义描述。
func WithArgDescs(descs ...string) FuncToolOption {
	return func(c *funcToolCfg) { c.argDescs = descs }
}

// WithToolKind sets the tool kind classification.
//
// WithToolKind 设置工具的分类类型。
func WithToolKind(kind kernel.ToolKind) FuncToolOption {
	return func(c *funcToolCfg) { c.kind = kind }
}

// WithToolSource sets the tool source identifier.
//
// WithToolSource 设置工具的来源标识。
func WithToolSource(source string) FuncToolOption {
	return func(c *funcToolCfg) { c.source = source }
}

// WithToolTags sets tags associated with the tool for filtering/grouping.
//
// WithToolTags 设置工具的关联标签，用于过滤/分组。
func WithToolTags(tags ...string) FuncToolOption {
	return func(c *funcToolCfg) { c.tags = tags }
}

// WithToolEffects declares the tool's side-effect classes for the
// permission stack (kernel.ToolMeta.Effects). Absent declarations keep the
// fail-closed conservative default (write + exec) — read-only tools MUST
// declare at least EffectRead.
//
// WithToolEffects 为权限栈声明工具的副作用类别（kernel.ToolMeta.Effects）。
// 未声明时保持 fail-closed 保守默认（写 + 执行）——只读工具必须至少声明
// EffectRead。
func WithToolEffects(effects ...kernel.ToolEffect) FuncToolOption {
	return func(c *funcToolCfg) { c.effects = append([]kernel.ToolEffect(nil), effects...) }
}

// WithToolTimeout declares the tool's cooperative execution budget in
// milliseconds (kernel.ToolMeta.TimeoutMs). The Runtime enforces the
// deadline per call while tools run concurrently; a timed-out call
// surfaces as a structured TOOL_TIMEOUT error. Only signal-forwarding
// tools should declare a budget — a tool that ignores context
// cancellation will not stop on timeout.
//
// WithToolTimeout 声明工具的协作式执行预算（毫秒，
// kernel.ToolMeta.TimeoutMs）。Runtime 在工具并发执行时为每次调用强制
// 截止时间；超时调用以结构化 TOOL_TIMEOUT 错误了结。只有转发信号的
// 工具应声明预算——忽略 context 取消的工具不会在超时后停止。
func WithToolTimeout(timeoutMs int64) FuncToolOption {
	return func(c *funcToolCfg) { c.timeout = timeoutMs }
}

// ToolFromFunc 从任意 Go 函数构造 FuncTool。
//
// 函数签名约束：
//   - 第一个参数必须是 context.Context
//   - 返回值可选：error、单个值、(T, error)
//   - 支持变参函数（自动映射为 JSON 数组）
//   - 支持单 struct 参数（自动展平为 JSON 属性）
//
// ToolFromFunc constructs a FuncTool from any Go function.
//
//	add, _ := tool.ToolFromFunc(func(ctx context.Context, a, b int) int { return a + b })
//	add, _ := tool.ToolFromFunc(func(ctx context.Context, req AddRequest) (AddResponse, error) { ... })
func ToolFromFunc(fn any, opts ...FuncToolOption) (*FuncTool, error) {
	cfg := &funcToolCfg{}
	for _, o := range opts {
		o(cfg)
	}

	fv := reflect.ValueOf(fn)
	ft := fv.Type()

	if ft.Kind() != reflect.Func {
		return nil, fmt.Errorf("tool: expected function, got %T", fn)
	}

	ftool := &FuncTool{
		fn:          fn,
		fnValue:     fv,
		fnType:      ft,
		name:        cfg.name,
		description: cfg.description,
		kind:        cfg.kind,
		source:      cfg.source,
		tags:        cfg.tags,
		effects:     cfg.effects,
		timeout:     cfg.timeout,
	}

	if ftool.name == "" {
		ftool.name = extractFuncName(fv)
	}

	numIn := ft.NumIn()
	paramStart := 0
	if numIn > 0 && ft.In(0) == contextType {
		paramStart = 1
	} else {
		return nil, fmt.Errorf("tool: first parameter must be context.Context")
	}

	params := numIn - paramStart
	if params == 1 {
		argType := ft.In(paramStart)
		if argType.Kind() == reflect.Struct || (argType.Kind() == reflect.Ptr && argType.Elem().Kind() == reflect.Struct) {
			ftool.singleStruct = true
		}
	}

	ftool.argSpecs = make([]argSpec, 0, params)
	for i := 0; i < params; i++ {
		argType := ft.In(paramStart + i)
		spec := argSpec{
			Type:     argType,
			Required: true,
		}

		if ftool.singleStruct {
			spec.IsStruct = true
			spec.Name = "input"
		} else {
			if i < len(cfg.argNames) {
				spec.Name = cfg.argNames[i]
			} else {
				spec.Name = fmt.Sprintf("arg%d", i)
			}
			if i < len(cfg.argDescs) {
				spec.Description = cfg.argDescs[i]
			}
		}
		ftool.argSpecs = append(ftool.argSpecs, spec)
	}

	switch ft.NumOut() {
	case 0:
		ftool.hasResult = false
	case 1:
		if ft.Out(0).Implements(errorType) {
			ftool.hasResult = false
		} else {
			ftool.hasResult = true
			ftool.resultIsStr = ft.Out(0).Kind() == reflect.String
		}
	case 2:
		ftool.hasResult = true
		ftool.resultIsStr = ft.Out(0).Kind() == reflect.String
		if !ft.Out(1).Implements(errorType) {
			return nil, fmt.Errorf("tool: second return value must be error")
		}
	default:
		return nil, fmt.Errorf("tool: function must return (result, error), (string, error), or error")
	}

	return ftool, nil
}

// MustToolFromFunc 类似 ToolFromFunc，失败时 panic。
//
// MustToolFromFunc is like ToolFromFunc but panics on failure.
func MustToolFromFunc(fn any, opts ...FuncToolOption) *FuncTool {
	t, err := ToolFromFunc(fn, opts...)
	if err != nil {
		panic(err)
	}
	return t
}

// Name returns the tool's name.
// Name 返回工具名称。
func (t *FuncTool) Name() string                                       { return t.name }
// Description returns the tool's description sent to the LLM.
// Description 返回发送给 LLM 的工具描述。
func (t *FuncTool) Description() string                                { return t.description }
// ListTools returns the tool itself as the only entry.
// ListTools 返回只包含工具自身的列表。
func (t *FuncTool) ListTools(_ context.Context) ([]kernel.Tool, error) { return []kernel.Tool{t}, nil }

// Schema returns the inferred JSON Schema for the tool arguments,
// building it lazily on first call; the returned map is a copy.
// Schema 返回工具参数的推断 JSON Schema，首次调用时惰性构建；返回的
// map 是副本。
func (t *FuncTool) Schema() map[string]any {
	t.schemaOnce.Do(func() {
		t.schema = t.buildSchema()
	})
	return cloneMap(t.schema)
}

// Run invokes the wrapped Go function with the JSON arguments and
// returns the formatted result.
// Run 使用 JSON 参数调用被包装的 Go 函数，并返回格式化后的结果。
func (t *FuncTool) Run(ctx context.Context, argsJSON string) (string, error) {
	callArgs := make([]reflect.Value, 0, 1+len(t.argSpecs))
	callArgs = append(callArgs, reflect.ValueOf(ctx))

	if len(t.argSpecs) == 0 {
		return t.callAndExtract(callArgs)
	}

	if t.singleStruct {
		return t.runWithStruct(ctx, argsJSON, callArgs)
	}

	return t.runWithMultiArgs(ctx, argsJSON, callArgs)
}

// ToolMeta returns the tool metadata (kind, source, tags, effects,
// timeout).
// ToolMeta 返回工具元数据（类型、来源、标签、副作用、超时）。
func (t *FuncTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{
		Kind:      t.kind,
		Source:    t.source,
		Tags:      t.tags,
		Effects:   t.effects,
		TimeoutMs: t.timeout,
	}
}

func (t *FuncTool) runWithStruct(ctx context.Context, argsJSON string, callArgs []reflect.Value) (string, error) {
	spec := t.argSpecs[0]
	structType := spec.Type

	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}

	ptr := reflect.New(structType)
	ptrIface := ptr.Interface()

	if argsJSON != "" && argsJSON != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), ptrIface); err != nil {
			return "", fmt.Errorf("tool %q: unmarshal args: %w", t.name, err)
		}
	}

	if spec.Type.Kind() == reflect.Ptr {
		callArgs = append(callArgs, ptr)
	} else {
		callArgs = append(callArgs, ptr.Elem())
	}

	return t.callAndExtract(callArgs)
}

func (t *FuncTool) runWithMultiArgs(ctx context.Context, argsJSON string, callArgs []reflect.Value) (string, error) {
	var rawMap map[string]json.RawMessage
	if argsJSON != "" && argsJSON != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), &rawMap); err != nil {
			return "", fmt.Errorf("tool %q: unmarshal args: %w", t.name, err)
		}
	}

	// Required arguments must be present as non-null values — empty or
	// absent arguments fail here, never silently zero-fill (C6 root cause:
	// the nil-map shortcut below used to bypass this check).
	for _, spec := range t.argSpecs {
		if !spec.Required || spec.IsStruct {
			continue
		}
		raw, exists := rawMap[spec.Name]
		if !exists || string(raw) == "null" {
			return "", fmt.Errorf("tool %q: missing required argument %q", t.name, spec.Name)
		}
	}

	for _, spec := range t.argSpecs {
		argValue, err := t.convertArg(rawMap, spec)
		if err != nil {
			return "", fmt.Errorf("tool %q: arg %q: %w", t.name, spec.Name, err)
		}
		callArgs = append(callArgs, argValue)
	}

	return t.callAndExtract(callArgs)
}

func (t *FuncTool) convertArg(rawMap map[string]json.RawMessage, spec argSpec) (reflect.Value, error) {
	// Required enforcement happens once, upstream in runWithMultiArgs;
	// this helper only converts present, non-null values.
	raw, exists := rawMap[spec.Name]
	if !exists || string(raw) == "null" {
		return reflect.Zero(spec.Type), nil
	}

	ptr := reflect.New(spec.Type)
	if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
		return reflect.Zero(spec.Type), fmt.Errorf("unmarshal: %w", err)
	}

	return ptr.Elem(), nil
}

func (t *FuncTool) callAndExtract(callArgs []reflect.Value) (string, error) {
	var results []reflect.Value
	if t.fnType.IsVariadic() {
		results = t.fnValue.CallSlice(callArgs)
	} else {
		results = t.fnValue.Call(callArgs)
	}

	if !t.hasResult {
		if len(results) == 1 {
			if err, ok := results[0].Interface().(error); ok {
				return "", err
			}
		}
		return "", nil
	}

	switch len(results) {
	case 1:
		return formatResult(results[0])
	case 2:
		if err, ok := results[1].Interface().(error); ok && err != nil {
			return "", err
		}
		return formatResult(results[0])
	default:
		return "", fmt.Errorf("tool: unexpected return count")
	}
}

func formatResult(v reflect.Value) (string, error) {
	if v.Kind() == reflect.String {
		return v.String(), nil
	}

	data, err := json.Marshal(v.Interface())
	if err != nil {
		return "", fmt.Errorf("tool: marshal result: %w", err)
	}
	return string(data), nil
}

func (t *FuncTool) buildSchema() map[string]any {
	if len(t.argSpecs) == 0 {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	seen := make(map[reflect.Type]bool)
	if t.singleStruct {
		spec := t.argSpecs[0]
		return buildStructSchema(spec.Type, seen)
	}

	properties := make(map[string]any)
	required := make([]any, 0)

	for _, spec := range t.argSpecs {
		propSchema := typeToSchema(spec.Type, seen)
		if spec.Description != "" {
			propSchema["description"] = spec.Description
		}
		properties[spec.Name] = propSchema
		if spec.Required {
			required = append(required, spec.Name)
		}
	}

	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func buildStructSchema(t reflect.Type, seen map[reflect.Type]bool) map[string]any {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return typeToSchema(t, seen)
	}

	if seen[t] {
		return map[string]any{"type": "object", "description": "circular reference"}
	}
	seen[t] = true

	properties := make(map[string]any)
	required := make([]any, 0)

	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		fieldSchema := buildFieldSchema(field, seen)
		if fieldSchema == nil {
			continue
		}

		name, opts := parseJSONTag(field.Tag.Get("json"))
		if name == "" {
			name = field.Name
		}
		if name == "-" {
			continue
		}

		if desc := field.Tag.Get("description"); desc != "" {
			fieldSchema["description"] = desc
		}

		properties[name] = fieldSchema

		if !opts["omitempty"] {
			required = append(required, name)
		}
	}

	result := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		result["required"] = required
	}

	return result
}

func buildFieldSchema(field reflect.StructField, seen map[reflect.Type]bool) map[string]any {
	ft := field.Type

	if ft.Kind() == reflect.Ptr {
		ft = ft.Elem()
	}

	return typeToSchema(ft, seen)
}

func typeToSchema(t reflect.Type, seen map[reflect.Type]bool) map[string]any {
	if t == rawMessageType {
		return map[string]any{"type": "object"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		elemType := t.Elem()
		if elemType.Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"}
		}
		return map[string]any{
			"type":  "array",
			"items": typeToSchema(elemType, seen),
		}
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			return map[string]any{
				"type":                 "object",
				"additionalProperties": typeToSchema(t.Elem(), seen),
			}
		}
		return map[string]any{"type": "object"}
	case reflect.Struct:
		return buildStructSchema(t, seen)
	case reflect.Interface:
		return map[string]any{}
	default:
		return map[string]any{"type": "string"}
	}
}

var (
	contextType    = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType      = reflect.TypeOf((*error)(nil)).Elem()
	rawMessageType = reflect.TypeOf(json.RawMessage{})
)

func extractFuncName(fn reflect.Value) string {
	if fn.Kind() == reflect.Func {
		if f := runtime.FuncForPC(fn.Pointer()); f != nil {
			name := f.Name()
			if idx := strings.LastIndex(name, "."); idx >= 0 {
				name = name[idx+1:]
			}
			return name
		}
	}
	return "unnamed_tool"
}

func parseJSONTag(tag string) (string, map[string]bool) {
	opts := make(map[string]bool)
	if tag == "" {
		return "", opts
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	for _, opt := range parts[1:] {
		opts[strings.TrimSpace(opt)] = true
	}
	return name, opts
}
