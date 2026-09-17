package kernel

import (
	"context"

	"github.com/go-gocel/gocel/core/types"
)

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// Tool defines the interface for a callable tool.
// Tools can be functions, MCP servers, skills, or sub-agents.
//
// Tool 定义了可调用工具的接口。工具可以是函数、MCP 服务、技能或子 Agent。
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Run(ctx context.Context, argsJSON string) (string, error)
	ToolMeta() ToolMeta
}

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// ToolProvider is a unified interface for anything that provides tools.
// FuncTool, AgentTool, Skill, and MCPSource all implement this.
//
// ToolProvider 是统一接口：任何能提供工具的东西都实现它。
// FuncTool、AgentTool、Skill、MCPSource 均实现此接口。
type ToolProvider interface {
	Name() string
	ListTools(ctx context.Context) ([]Tool, error)
}

// ToolWithResult is an optional capability a Tool may implement to attach
// multimodal content parts to its textual result. The orchestration layer
// detects this interface and builds a tool message carrying both the text and
// the parts; tools that only produce text keep implementing Tool alone and
// are unaffected (zero-value = old behavior).
//
// Current scope: text + image parts are carried through to the model. Audio,
// video and file parts are not yet serialized by the OpenAI provider and are
// dropped fail-closed — full support is tracked separately.
//
// The returned parts slice is owned by the framework: the tool must not
// mutate or reuse it after returning (mirroring the Run string contract).
//
// ToolWithResult 是 Tool 的可选能力：在文本结果之外附加多模态内容片段。
// 编排层检测到该接口后，会构造同时携带文本与片段的工具消息；仅产出文本
// 的工具继续只实现 Tool，行为不受影响（零值即旧行为）。
//
// 当前范围：文本 + 图片片段会透传给模型。音频/视频/文件片段尚未被 OpenAI
// 提供者序列化，会被丢弃（fail-closed）——完整支持另行跟进。
//
// 返回的 parts 切片归框架所有：工具不得在返回后修改或复用（与 Run 返回
// string 的契约一致）。
type ToolWithResult interface {
	Tool
	// RunWithResult runs the tool and returns the textual result together
	// with optional multimodal content parts. The parts are carried on the
	// tool message so the model can see them on the next step.
	//
	// RunWithResult 执行工具，返回文本结果与可选的多模态内容片段。
	// 片段随工具消息携带，使模型在下一步能看到它们。
	RunWithResult(ctx context.Context, argsJSON string) (string, []types.ContentPart, error)
}

// ToolSource is a type alias kept for backward compatibility.
// Deprecated: use ToolProvider instead.
//
// ToolSource 是为向后兼容保留的类型别名。已弃用：请改用 ToolProvider。
type ToolSource = ToolProvider

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// ToolRegistry manages a flat collection of tools.
// Sources are flattened at Runtime.Register time, not stored in the registry.
//
// ToolRegistry 管理扁平工具集合。来源在 Runtime.Register 时展平。
type ToolRegistry interface {
	List(ctx context.Context) []Tool
	Get(ctx context.Context, name string) Tool
	Add(ctx context.Context, tool Tool) error
	Remove(ctx context.Context, name string) error
}

// ToolKind classifies tools by their origin.
// ToolKind 按来源分类工具。
type ToolKind string

const (
	// ToolKindFunction marks tools backed by plain Go functions.
	// ToolKindFunction 表示由普通 Go 函数实现的工具。
	ToolKindFunction ToolKind = "function"
	// ToolKindMCP marks tools exposed through an MCP server.
	// ToolKindMCP 表示通过 MCP 服务暴露的工具。
	ToolKindMCP      ToolKind = "mcp"
	// ToolKindSkill marks tools provided by a skill.
	// ToolKindSkill 表示由技能（skill）提供的工具。
	ToolKindSkill    ToolKind = "skill"
	// ToolKindBuiltin marks tools built into the runtime.
	// ToolKindBuiltin 表示运行时内置的工具。
	ToolKindBuiltin  ToolKind = "builtin"
	// ToolKindAgent marks tools that delegate to a sub-agent.
	// ToolKindAgent 表示委托给子 Agent 的工具。
	ToolKindAgent    ToolKind = "agent"
)

// ToolMeta carries metadata about a tool including its kind, source, and tags.
// ToolMeta 携带工具的元数据，包括种类、来源和标签。
type ToolMeta struct {
	Kind    ToolKind
	Source  string
	Tags    []string
	Version string
	// Effects declares the side-effect classes of this tool. Permission
	// modules read it to enforce the session's permission tier; an empty
	// slice means "undeclared" and is treated conservatively (see
	// EffectiveEffects).
	//
	// Effects 声明本工具的副作用类别。权限模块读取它执行权限档位；
	// 空切片表示"未声明"，按保守档处理（见 EffectiveEffects）。
	Effects []ToolEffect
	// TimeoutMs declares the tool's cooperative execution budget in
	// milliseconds; 0 = no declared budget. The Runtime enforces it when
	// executing the tool (the declaration lives here, the enforcement in
	// the mechanism — DSH tool-call-timeout separation). A tool that
	// ignores its context cancellation will not stop on timeout; only
	// signal-forwarding tools should declare a budget.
	//
	// TimeoutMs 声明工具的协作式执行预算（毫秒）；0 = 未声明。Runtime
	// 执行工具时强制该预算（声明在此处、强制在机制中——DSH 工具超时
	// 分离）。忽略 context 取消的工具不会在超时后停止；只有转发信号的
	// 工具才应声明预算。
	TimeoutMs int64
}

// ToolEffect classifies the side effect of a tool call. Tools declare their
// effects in ToolMeta; the permission stack (FilePolicy tier + ApprovalPolicy)
// consumes the declaration to decide whether a call is allowed.
//
// ToolEffect 分类工具调用的副作用。工具在 ToolMeta 中声明；权限栈
// （FilePolicy 档位 + ApprovalPolicy）消费该声明裁决是否放行。
type ToolEffect int

const (
	// EffectRead reads files, search results, or environment data.
	// EffectRead 读取文件、搜索结果或环境数据。
	EffectRead ToolEffect = 1 << iota
	// EffectWrite creates, modifies, moves, or deletes files.
	// EffectWrite 创建、修改、移动或删除文件。
	EffectWrite
	// EffectExec runs programs, scripts, or shell commands.
	// EffectExec 运行程序、脚本或 shell 命令。
	EffectExec
	// EffectNetwork makes network requests beyond the model itself.
	// EffectNetwork 发起模型本身之外的网络请求。
	EffectNetwork
	// EffectUserData reads or writes user-owned data (credentials, settings).
	// EffectUserData 读写用户自有数据（凭据、设置）。
	EffectUserData
)

// effectiveFallback is the conservative default for tools that declare no
// effects: write + exec, the two classes a permission tier may deny.
var effectiveFallback = []ToolEffect{EffectWrite, EffectExec}

// EffectiveEffects returns the tool's declared effects, or the conservative
// default (write + exec) when none are declared. Fail-closed: a tool that
// forgets its declaration is treated as if it could mutate and execute.
// The returned slice is a copy — callers may not mutate the shared fallback.
//
// EffectiveEffects 返回工具声明的副作用；未声明时返回保守默认
// （写 + 执行）。fail-closed：忘记声明的工具按"可能修改与执行"处理。
// 返回的是副本——调用方不得篡改共享的默认值。
func EffectiveEffects(t Tool) []ToolEffect {
	effects := t.ToolMeta().Effects
	if len(effects) == 0 {
		return append([]ToolEffect(nil), effectiveFallback...)
	}
	return append([]ToolEffect(nil), effects...)
}

// ❄️ FROZEN — Stable type. Fields and semantics must not change.
//
// ToolInfo provides a summary of a tool for LLM function-calling schemas.
// ToolInfo 提供工具摘要，用于 LLM 函数调用 schema。
type ToolInfo struct {
	Name        string
	Description string
	Parameters  map[string]any
	Kind        ToolKind
}

// ToolFromInfo converts a Tool to its ToolInfo representation.
// ToolFromInfo 将 Tool 转换为 ToolInfo 表示。
func ToolFromInfo(t Tool) *ToolInfo {
	return &ToolInfo{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters:  t.Schema(),
		Kind:        t.ToolMeta().Kind,
	}
}

// ToolFromTools converts a slice of Tools to ToolInfo slice.
// ToolFromTools 将工具切片转换为 ToolInfo 切片。
func ToolFromTools(tools []Tool) []*ToolInfo {
	infos := make([]*ToolInfo, len(tools))
	for i, t := range tools {
		infos[i] = ToolFromInfo(t)
	}
	return infos
}

// NormalizeToolCall normalizes a tool call's ID, type, and arguments.
// Some providers omit optional fields; this ensures consistency.
//
// NormalizeToolCall 标准化工具调用的 ID、类型和参数。确保跨提供者一致性。
func NormalizeToolCall(tc *types.ToolCall) {
	if tc == nil {
		return
	}
	if tc.Type == "" {
		tc.Type = "function"
	}
	if tc.ID == "" {
		tc.ID = "call_" + types.SessionID()
	}
	if tc.Function.Arguments == "" {
		tc.Function.Arguments = "{}"
	}
}

// ToolCallOption configures a tool call.
// ToolCallOption 是工具调用的配置选项函数。
type ToolCallOption func(*types.ToolCall)

// WithToolCallKind sets the tool call kind (e.g. "function").
// WithToolCallKind 设置工具调用类型（如 "function"）。
func WithToolCallKind(kind string) ToolCallOption {
	return func(tc *types.ToolCall) {
		tc.Type = kind
	}
}
