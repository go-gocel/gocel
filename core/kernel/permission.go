package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-gocel/gocel/core/types"
)

// FilePolicy is the file-effect boundary of a session. It combines the
// session's permission tier with the workspace root to answer one question:
// is this file operation allowed? Implementations resolve symlinks and
// normalise paths themselves — the caller passes the operation and the path
// the tool intends to touch.
//
// Denials use the closed PolicyDenial vocabulary: consumers distinguish
// policy refusals from operational failures with IsPolicyDenial, mirroring
// DSH's structured FS_SANDBOX_DENIED-style codes instead of string matching.
//
// FilePolicy 是会话的文件操作边界：结合权限档位与工作区根目录回答
// "该文件操作是否允许"。实现自行解析符号链接与规范化路径——
// 调用方只传操作类型与工具要触碰的路径。
//
// 拒绝使用封闭的 PolicyDenial 词汇：消费方用 IsPolicyDenial 区分
// 策略拒绝与运行故障，对应 DSH 结构化的 FS_SANDBOX_DENIED 类代码，
// 不做字符串匹配。
type FilePolicy interface {
	// Mode returns the session's current permission tier.
	// Mode 返回会话当前的权限档位。
	Mode() types.PermissionMode

	// Check reports whether op on path is allowed at the current tier.
	// A nil return means allowed; a non-nil error names the denied operation.
	// Fail-closed: anything not explicitly allowed is denied.
	//
	// Check 报告当前档位下对 path 执行 op 是否允许。
	// 返回 nil 表示允许；非 nil 错误指明被拒绝的操作。
	// fail-closed：未明确允许的一律拒绝。
	Check(op types.FileOp, path string) error
}

// PolicyDenial is the closed error vocabulary for permission-tier refusals.
// FilePolicy implementations return it (wrapped or direct); consumers
// classify refusals with IsPolicyDenial and read Op/Path/Mode structurally.
//
// PolicyDenial 是权限档位拒绝的封闭错误词汇。FilePolicy 实现直接或包装
// 返回它；消费方用 IsPolicyDenial 分类，结构化读取 Op/Path/Mode。
type PolicyDenial struct {
	// Op is the denied file operation.
	Op types.FileOp
	// Path is the path the operation targeted.
	Path string
	// Mode is the permission tier the denial was decided at.
	Mode types.PermissionMode
	// Reason describes the concrete cause (read-only tier, outside root, …).
	Reason string
}

// Error renders the denial in a stable machine-readable shape:
// "policy denial: <op> <path> at <mode>: <reason>".
// Error 以稳定的机器可读格式渲染拒绝信息。
func (d *PolicyDenial) Error() string {
	return fmt.Sprintf("policy denial: %s %s at %s: %s",
		d.Op, d.Path, d.Mode, d.Reason)
}

// IsPolicyDenial reports whether err is (or wraps) a PolicyDenial.
// IsPolicyDenial 报告 err 是否为（或包装了）PolicyDenial。
func IsPolicyDenial(err error) bool {
	var d *PolicyDenial
	return errors.As(err, &d)
}

// ApprovalDecision is the outcome of an approval policy evaluation for a
// single tool call.
//
// ApprovalDecision 是对单次工具调用执行审批策略的结论。
type ApprovalDecision int

const (
	// ApprovalAuto allows the call without asking the user.
	// ApprovalAuto 无需询问用户，直接放行。
	ApprovalAuto ApprovalDecision = iota
	// ApprovalAsk defers to the user through the HITL channel.
	// ApprovalAsk 经 HITL 通道交由用户裁决。
	ApprovalAsk
	// ApprovalAllowedOnce allows exactly the evaluated call once, without
	// widening the session's permission tier — the one-shot escalation
	// grant (DSH's `allowed-once`). The next identical call is re-evaluated.
	//
	// ApprovalAllowedOnce 仅放行本次被评估的调用一次，不提升会话档位——
	// 一次性提权授权（DSH 的 allowed-once）。下一次相同调用重新评估。
	ApprovalAllowedOnce
	// ApprovalDeny denies the call without asking.
	// ApprovalDeny 无需询问，直接拒绝。
	ApprovalDeny
)

// String returns the machine-readable name of the decision.
// String 返回决策的机器可读名称。
func (d ApprovalDecision) String() string {
	switch d {
	case ApprovalAuto:
		return "auto"
	case ApprovalAsk:
		return "ask"
	case ApprovalAllowedOnce:
		return "allowed-once"
	case ApprovalDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// DefaultApprovalFor maps a permission tier to its preset approval
// disposition, mirroring DSH's permission-presets: the ordinary tiers run
// under ask; danger-full-access never asks; an unknown tier fails closed.
//
// DefaultApprovalFor 把权限档位映射为预设审批处置，对应 DSH 的
// permission-presets：普通档位走 ask；danger-full-access 从不询问；
// 未知档位 fail-closed。
func DefaultApprovalFor(mode types.PermissionMode) ApprovalDecision {
	switch mode {
	case types.PermissionReadOnly, types.PermissionWorkspaceWrite:
		return ApprovalAsk
	case types.PermissionDangerFullAccess:
		return ApprovalAuto
	default:
		return ApprovalDeny
	}
}

// DeniedError carries a human denial together with the host's feedback
// text. Askers return it alongside ApprovalDeny so consuming modules can
// surface the reason to the model instead of a generic rejection. It is
// the closed denial-with-feedback vocabulary of the approval seam.
//
// DeniedError 携带人工拒绝与宿主反馈文本。Asker 与 ApprovalDeny 一并返回，
// 消费模块得以把原因呈现给模型，而不是笼统的拒绝。它是审批缝的封闭
// 「拒绝+反馈」词汇。
type DeniedError struct {
	Feedback string
}

// Error returns the denial's feedback text as the error message.
// Error 返回拒绝的反馈文本作为错误信息。
func (e *DeniedError) Error() string { return e.Feedback }

// IsDeniedError reports whether err is (or wraps) a DeniedError, returning
// its feedback text.
//
// IsDeniedError 判断 err 是否为（或包装了）DeniedError，返回反馈文本。
func IsDeniedError(err error) (string, bool) {
	var d *DeniedError
	if errors.As(err, &d) {
		return d.Feedback, true
	}
	return "", false
}

// ApprovalRequest carries everything an approval policy needs to decide a
// tool call: which tool, what effects it declares, its arguments, and an
// optional justification for one-shot escalation beyond the permission tier.
//
// ApprovalRequest 携带审批策略裁决工具调用所需的全部信息：工具名、
// 声明的副作用、参数，以及可选的越档单次提权理由。
type ApprovalRequest struct {
	// ToolName is the name of the tool being called.
	ToolName string
	// Effects is the tool's declared (or conservatively defaulted) effects.
	Effects []ToolEffect
	// Args is the raw tool-call arguments JSON.
	Args string
	// Reason is the justification for one-shot escalation. Empty for calls
	// that fit the current tier. Escalation requests MUST carry a non-empty
	// Reason — an escalation without justification is rejected (DSH requires
	// sandbox_permissions and justification in pairs).
	//
	// Reason 是单次提权的理由；符合当前档位的调用为空。提权请求必须
	// 携带非空 Reason——无理由的提权被拒绝（DSH 要求
	// sandbox_permissions 与 justification 成对出现）。
	Reason string
}

// ApprovalPolicy decides whether a tool call proceeds, needs user approval,
// or is denied. The default policy for fresh sessions is ask; the approval
// stack (module/permission + module/hitl in gocel) wires an implementation
// to the Host.
//
// Contract semantics (borrowed from DSH's approval seam):
//   - Policies are pure decision functions — they must not block waiting for
//     the user; ApprovalAsk defers the wait to the HITL channel.
//   - Fail-closed: an error return is treated by consumers as ApprovalDeny.
//   - Every ApprovalAsk must be settled and reported through the Runtime's
//     Decision hooks (FireDecision) as an asked→decided pair, so audit and
//     observability see complete trails.
//
// ApprovalPolicy 裁决工具调用是放行、需审批还是拒绝。新会话默认 ask；
// 审批栈（gocel 的 module/permission + module/hitl）把实现接入 Host。
//
// 契约语义（借鉴自 DSH 审批缝）：
//   - 策略是纯决策函数——不得阻塞等待用户；ApprovalAsk 把等待交给 HITL 通道。
//   - fail-closed：消费方把策略返回的错误视为 ApprovalDeny。
//   - 每次 ApprovalAsk 都必须了结并经 Runtime 的 Decision 钩子
//     （FireDecision）上报为 asked→decided 配对，让审计与可观测性
//     看到完整轨迹。
type ApprovalPolicy interface {
	// Decide evaluates the request and returns a decision.
	// Decide 评估请求并返回裁决。
	Decide(ctx context.Context, req *ApprovalRequest) (ApprovalDecision, error)
}

// ── Delegation context（委派上下文）─────────────────────────────────────
//
// 委派策略的传递通道：父 Agent 把审批策略钉在委派边界（DSH 委派即收权：
// 子代理的审批策略在创建时快照、钉死为 never 或继承），子代理执行时
// 从 context 读取。机制（orchestrate 注入/快照）与策略（gocel 定义
// 具体 ApprovalPolicy）在此分离——core 只提供传递原语，不解释策略。

type delegationKey struct{}

// WithDelegatedApproval pins an approval policy onto a delegated execution
// context. The policy is captured at the delegation boundary (spawn/fork)
// and consulted by approval modules for every tool call the child makes —
// a child's effective approval disposition never comes from the parent's
// live configuration (DSH delegation policy inheritance).
//
// WithDelegatedApproval 把审批策略钉在委派执行上下文上。策略在委派边界
// （spawn/fork）捕获，子代理的每次工具调用都由审批模块读取——子代理的
// 有效审批处置绝不来自父级的实时配置（DSH 委派策略继承）。
func WithDelegatedApproval(parent context.Context, policy ApprovalPolicy) context.Context {
	return context.WithValue(parent, delegationKey{}, policy)
}

// DelegatedApprovalFromContext returns the approval policy pinned at the
// delegation boundary, or nil when the call runs outside a delegation.
//
// DelegatedApprovalFromContext 返回委派边界钉住的审批策略；
// 非委派执行返回 nil。
func DelegatedApprovalFromContext(ctx context.Context) ApprovalPolicy {
	p, _ := ctx.Value(delegationKey{}).(ApprovalPolicy)
	return p
}
