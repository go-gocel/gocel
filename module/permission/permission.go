// Package permission enforces the session's permission tier at the tool-call
// boundary, consuming the three P0 contracts together: ToolMeta.Effects
// (what the call does), FilePolicy (where the tier confines it), and
// ApprovalPolicy (whether the call may proceed). It is the DSH
// sandbox-policy + approval counterpart:
//
//   - A call whose effects include writes is containment-checked against the
//     FilePolicy. A denial is not silently dropped: it escalates to the host
//     as a one-shot ask (allowed-once) — the session tier never widens.
//   - Calls that fit the tier still pass through the ApprovalPolicy
//     (ask/auto/deny preset). An ask blocks until the host answers through
//     the Asker channel; cancellation or an Asker error denies (fail-closed).
//   - Every denial or settled ask is reported through the Runtime's Decision
//     hooks as an asked→decided pair, so audit and observability see the
//     complete trail.
//
// Package permission 在工具调用边界执行会话权限档位，同时消费三个 P0
// 契约：ToolMeta.Effects（调用做什么）、FilePolicy（档位约束哪里）、
// ApprovalPolicy（调用能否放行）。它是 DSH sandbox-policy + approval 的
// 对应物：
//
//   - 含写副作用的调用先做 FilePolicy 包含性检查。拒绝不被静默吞掉：
//     以一次性询问（allowed-once）升级到宿主——会话档位绝不放宽。
//   - 符合档位的调用仍过 ApprovalPolicy（ask/auto/deny 预设）。ask 阻塞
//     直到宿主经 Asker 通道回答；取消或 Asker 出错即拒绝（fail-closed）。
//   - 每次拒绝或了结的 ask 都经 Runtime 的 Decision 钩子按
//     asked→decided 配对上报，审计与可观测性看到完整轨迹。
package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Asker waits for the host's decision on a tool call. It must block until a
// decision is available or ctx is done; returning an error denies the call
// (fail-closed). The default asker is nil: calls that need a decision are
// denied — products wire their answerer channel here.
//
// Asker 等待宿主对工具调用的裁决。必须阻塞到有结果或 ctx 结束；
// 返回错误即拒绝（fail-closed）。默认 asker 为 nil：需要裁决的调用被
// 拒绝——产品在此接入各自的 answerer 通道。
type Asker func(ctx context.Context, req *kernel.ApprovalRequest) (kernel.ApprovalDecision, error)

// NeverApprovalPolicy is the delegation-fixed disposition: it denies every
// call deterministically, without consulting any channel (DSH approval
// policy 'never'). Products pin it onto a subagent delegation so the child
// can never widen its scope through approval — CI and unattended children
// run under it by construction.
//
// NeverApprovalPolicy 是委派钉死的处置：确定性拒绝一切调用，不咨询任何
// 通道（DSH 审批策略 never）。产品把它钉在子代理委派上，子代理就永远
// 无法经审批扩权——CI 与无人值守子代理天然运行在其下。
type NeverApprovalPolicy struct{}

// Decide implements kernel.ApprovalPolicy.
// Decide 实现 kernel.ApprovalPolicy：确定性拒绝每次调用。
func (NeverApprovalPolicy) Decide(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
	return kernel.ApprovalDeny, nil
}

// Option configures the module.
// Option 配置模块。
type Option func(*Module)

// WithPaths replaces the generic path extractor: fn returns the file paths a
// tool call intends to touch, used for FilePolicy containment checks, or an
// error when the arguments cannot be parsed (fail-closed — unparseable
// arguments must deny, never silently pass containment).
// Products with schema-specific tool shapes supply their own extractor; the
// default scans the arguments JSON for string values under path-like keys.
//
// WithPaths 替换通用路径提取器：fn 返回工具调用意图触碰的文件路径，
// 用于 FilePolicy 包含性检查；参数无法解析时返回错误（fail-closed——
// 不可解析的参数必须拒绝，绝不静默通过包含性检查）。具有特定工具
// schema 的产品提供自己的提取器；默认扫描参数 JSON 中路径类键名下的
// 字符串值。
func WithPaths(fn func(toolName, args string) ([]string, error)) Option {
	return func(m *Module) { m.paths = fn }
}

// Module enforces the tier + approval stack on every tool call.
// Module 在每次工具调用上执行档位 + 审批栈。
type Module struct {
	file     kernel.FilePolicy
	approval kernel.ApprovalPolicy
	asker    Asker
	paths    func(toolName, args string) ([]string, error)
}

// New builds the enforcement module. asker may be nil (fail-closed); file
// and approval must be non-nil — a nil policy denies everything rather than
// opening a hole.
//
// New 构建执行模块。asker 可为 nil（fail-closed）；file 与 approval 必须
// 非 nil——nil 策略拒绝一切，而不是打开缺口。
func New(file kernel.FilePolicy, approval kernel.ApprovalPolicy, asker Asker, opts ...Option) *Module {
	m := &Module{file: file, approval: approval, asker: asker, paths: defaultPaths}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Register installs the permission enforcement hook on the given runtime.
// Register 在给定 runtime 上安装权限执行钩子。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnToolCall(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		if info == nil {
			return ctx, info, nil
		}

		// Delegation-fixed disposition: a subagent spawned under a pinned
		// approval policy (DSH delegation policy inheritance) is decided by
		// THAT policy alone — the session tier, the host asker, and the
		// session's live approval disposition never apply to a delegated
		// call. The policy is a pure decision function (never blocks);
		// any error denies (fail-closed).
		//
		// The FilePolicy containment axis ALWAYS applies — delegation pins
		// the approval disposition, never the session tier. The old code
		// returned before containment, so an auto/allowed-once delegated
		// policy widened the tier to unlimited writes (verified defect).
		if dp := kernel.DelegatedApprovalFromContext(ctx); dp != nil {
			req := &kernel.ApprovalRequest{ToolName: info.Name, Effects: info.Effects, Args: info.Args}
			if handled, err := m.contain(ctx, rt, info, req, false); err != nil {
				return ctx, nil, err
			} else if handled {
				return ctx, info, nil
			}
			dec, err := dp.Decide(ctx, req)
			if err != nil {
				dec = kernel.ApprovalDeny
			}
			switch dec {
			case kernel.ApprovalAuto, kernel.ApprovalAllowedOnce:
				return ctx, info, nil
			case kernel.ApprovalAsk:
				// A delegated policy must not block on the parent's HITL
				// channel — no one is watching it. Fail closed.
				m.fire(rt, ctx, info, "policy_deny", "delegated approval policy asks but no answerer watches a child")
				return ctx, nil, fmt.Errorf("permission: delegated tool %q requires approval that cannot be answered in a child", info.Name)
			default:
				m.fire(rt, ctx, info, "policy_deny", "delegated approval policy denied the call")
				return ctx, nil, fmt.Errorf("permission: delegated tool %q denied by the pinned approval policy", info.Name)
			}
		}

		effects := info.Effects
		if len(effects) == 0 {
			// The Runtime fills Effects from the tool registry; an engine
			// bypassing that enrichment yields an empty set, which must
			// fail closed — same conservative default as
			// kernel.EffectiveEffects for undeclared tools.
			effects = []kernel.ToolEffect{kernel.EffectWrite, kernel.EffectExec}
		}
		req := &kernel.ApprovalRequest{ToolName: info.Name, Effects: effects, Args: info.Args}

		// 1. Containment: write-effect calls are checked against the
		//    FilePolicy. A denial escalates to the host ONLY when the
		//    approval policy would ask (ask mode); under auto/never modes a
		//    denial is a hard block — never silently dropped, never
		//    tier-widening. A settled escalation skips the approval stack.
		if handled, err := m.contain(ctx, rt, info, req, true); err != nil {
			return ctx, nil, err
		} else if handled {
			return ctx, info, nil
		}

		// 2. The approval stack's disposition for calls that fit the tier.
		if m.approval == nil {
			return m.decide(ctx, rt, info, req, "no approval policy configured")
		}
		dec, err := m.approval.Decide(ctx, req)
		if err != nil {
			dec = kernel.ApprovalDeny // fail-closed
		}
		switch dec {
		case kernel.ApprovalAuto, kernel.ApprovalAllowedOnce:
			return ctx, info, nil
		case kernel.ApprovalDeny:
			m.fire(rt, ctx, info, "policy_deny", "approval policy denied the call")
			return ctx, nil, fmt.Errorf("permission: tool %q denied by approval policy", info.Name)
		case kernel.ApprovalAsk:
			return m.decide(ctx, rt, info, req, "the approval policy asks the host")
		default:
			m.fire(rt, ctx, info, "policy_deny", "unknown approval decision")
			return ctx, nil, fmt.Errorf("permission: unknown approval decision for tool %q", info.Name)
		}
	})
}

// contain runs the FilePolicy containment axis for write-effect calls.
// Unparseable arguments deny (fail-closed — the old default extractor
// returned nil on parse failure, silently skipping containment).
// escalate=true lets a denial reach the host asker (ask mode); delegated
// calls pass escalate=false so a child never waits on an unwatched channel.
//
// The bool reports whether the containment axis SETTLED the call (hard
// block, or host escalation that decided) — the approval stack must not
// run again in that case (the old flow double-asked after an escalation).
func (m *Module) contain(ctx context.Context, rt kernel.HookRegistrar, info *kernel.ToolCallInfo, req *kernel.ApprovalRequest, escalate bool) (bool, error) {
	if !hasEffect(req.Effects, kernel.EffectWrite) {
		return false, nil
	}
	paths, err := m.paths(info.Name, info.Args)
	if err != nil {
		m.fire(rt, ctx, info, "policy_deny", "unparseable arguments: "+err.Error())
		return true, fmt.Errorf("permission: %v", err)
	}
	for _, path := range paths {
		if err := m.checkFile(path); err != nil {
			if escalate && m.shouldAsk(ctx, req) {
				_, _, aerr := m.decide(ctx, rt, info, req, err.Error())
				return true, aerr
			}
			m.fire(rt, ctx, info, "policy_deny", err.Error())
			return true, fmt.Errorf("permission: %v", err)
		}
	}
	return false, nil
}

// shouldAsk reports whether a denied call escalates to the host: only when
// the approval policy's disposition is ask. Auto/never modes block hard;
// a policy error fails closed (no ask, hard block).
func (m *Module) shouldAsk(ctx context.Context, req *kernel.ApprovalRequest) bool {
	if m.approval == nil {
		return false
	}
	dec, err := m.approval.Decide(ctx, req)
	if err != nil {
		return false
	}
	return dec == kernel.ApprovalAsk
}

// checkFile runs the FilePolicy containment check for one path; nil means
// the path fits the tier.
func (m *Module) checkFile(path string) error {
	if path == "" {
		return nil
	}
	if m.file == nil {
		return &kernel.PolicyDenial{Op: types.FileOpWrite, Path: path, Reason: "no file policy configured"}
	}
	return m.file.Check(types.FileOpWrite, path)
}

// decide escalates the call to the host (one-shot): the asker's decision is
// final for THIS call only. Missing asker, asker error, or context
// cancellation all deny — fail-closed.
func (m *Module) decide(ctx context.Context, rt kernel.HookRegistrar, info *kernel.ToolCallInfo, req *kernel.ApprovalRequest, reason string) (context.Context, *kernel.ToolCallInfo, error) {
	req.Reason = reason
	if m.asker == nil {
		m.fire(rt, ctx, info, "rejected", "no approval answerer configured")
		return ctx, nil, fmt.Errorf("permission: tool %q needs host approval but no answerer is configured", info.Name)
	}
	dec, err := m.asker(ctx, req)
	// A deny-with-feedback (kernel.DeniedError) is a REJECTION carrying the
	// host's reason — never an approval-channel failure.
	if fb, ok := kernel.IsDeniedError(err); ok && dec == kernel.ApprovalDeny {
		m.fire(rt, ctx, info, "rejected", fb)
		return ctx, nil, fmt.Errorf("permission: tool %q rejected by the host: %s", info.Name, fb)
	}
	if err != nil || ctx.Err() != nil {
		m.fire(rt, ctx, info, "rejected", "approval channel error or cancellation")
		if err == nil {
			err = ctx.Err()
		}
		return ctx, nil, fmt.Errorf("permission: approval for tool %q failed: %w", info.Name, err)
	}
	switch dec {
	case kernel.ApprovalAllowedOnce, kernel.ApprovalAuto:
		m.fire(rt, ctx, info, "approved", reason)
		return ctx, info, nil
	default:
		m.fire(rt, ctx, info, "rejected", reason)
		return ctx, nil, fmt.Errorf("permission: tool %q rejected by the host", info.Name)
	}
}

// fire reports the decision through the runtime's Decision hooks (the
// asked→decided pairing audit sees).
func (m *Module) fire(rt kernel.HookRegistrar, ctx context.Context, info *kernel.ToolCallInfo, decision, reason string) {
	if f, ok := rt.(interface {
		FireDecision(context.Context, *kernel.DecisionInfo) error
	}); ok {
		_ = f.FireDecision(ctx, &kernel.DecisionInfo{
			InvocationID: info.InvocationID,
			AgentName:    info.AgentName,
			StepIndex:    info.StepIndex,
			Tool:         info.Name,
			Args:         info.Args,
			Decision:     decision,
			Reason:       reason,
		})
	}
}

func hasEffect(effects []kernel.ToolEffect, want kernel.ToolEffect) bool {
	for _, e := range effects {
		if e == want {
			return true
		}
	}
	return false
}

// defaultPaths scans the arguments JSON for string values under path-like
// keys (top-level and nested). It is deliberately generic: schema-precise
// extraction is a product concern (WithPaths). Unparseable arguments are
// an error — a safety guard must not silently pass through unparseable
// input (fail-closed).
func defaultPaths(_, args string) ([]string, error) {
	if args == "" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return nil, fmt.Errorf("unparseable tool arguments: %w", err)
	}
	var out []string
	collectPaths(v, &out)
	return out, nil
}

func collectPaths(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			kl := strings.ToLower(k)
			if s, ok := val.(string); ok && isPathKey(kl) {
				*out = append(*out, s)
				continue
			}
			collectPaths(val, out)
		}
	case []any:
		for _, item := range t {
			collectPaths(item, out)
		}
	}
}

func isPathKey(k string) bool {
	return k == "path" || k == "file" || k == "filepath" || k == "dir" ||
		k == "directory" || k == "target" || k == "root" || k == "workdir" ||
		strings.HasSuffix(k, "_path") || strings.HasSuffix(k, "_dir")
}
