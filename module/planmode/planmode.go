// Package planmode implements the DSH plan mode: while a session is in
// SessionModePlan the agent explores read-only and must submit a plan through
// the exit_plan_mode tool; a host (HITL) approval flips the session out of
// plan mode and mutations become possible. The gate is the module, the
// gate-crossing call is the tool — one package, one feature.
//
// Closed error vocabulary: plan-mode refusals are *Denial values (see
// IsDenial) so consumers can distinguish policy refusals from operational
// failures without string matching. An approval failure NEVER exits plan
// mode; a denial keeps the session read-only.
//
// Package planmode 实现 DSH plan 模式：会话处于 SessionModePlan 时 agent
// 只读探索，必须通过 exit_plan_mode 提交计划；宿主（HITL）批准后会话退出
// plan 模式，变更才被允许。门禁是模块，跨越门禁的调用是工具——一个包，
// 一个特性。
//
// 封闭错误词汇：plan 模式拒绝是 *Denial（见 IsDenial），消费方无需字符串
// 匹配即可区分策略拒绝与运行失败。审批失败绝不退出 plan 模式；拒绝保持
// 会话只读。
package planmode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// DefaultToolName is the gate-crossing tool's name (DSH vocabulary).
// DefaultToolName 是跨越门禁的工具名（DSH 词汇）。
const DefaultToolName = "exit_plan_mode"

// Config wires the module to the consumer's session authority.
// Config 把模块接到消费方的会话权威。
type Config struct {
	// Mode reads the authoritative session mode (host session store).
	// Mode 读取权威会话模式（宿主会话存储）。
	Mode func() types.SessionMode
	// Ask submits the plan for a host decision. It may block (HITL) but must
	// return a settled decision: ApprovalAuto/ApprovalAllowedOnce approve,
	// ApprovalDeny rejects. A returned ApprovalAsk is a miswired seam and
	// fails closed.
	//
	// Ask 把计划提交宿主裁决。可以阻塞（HITL），但必须返回已裁决结果：
	// Auto/AllowedOnce 通过，Deny 拒绝。返回 Ask 说明 seam 接线错误，
	// fail-closed。
	Ask func(ctx context.Context, req *kernel.ApprovalRequest) (kernel.ApprovalDecision, error)
	// OnApprove flips the session out of plan mode (and persists it). Called
	// exactly once per approval.
	//
	// OnApprove 使会话退出 plan 模式（并持久化）。每次批准恰好调用一次。
	OnApprove func(plan string)
	// ToolName overrides the plan tool's name; defaults to DefaultToolName.
	ToolName string
	// Log, when set, makes the plan state durable in the session event log
	// (DSH plan-mode log-as-state): entering plan mode appends a log-only
	// plan/mode event, approval appends the exit event, and Mode folds the
	// log when no live callback is provided. The log is the truth; a
	// restart, resume, or fork recovers plan state from it.
	//
	// Log 非 nil 时，把 plan 状态持久化到会话事件日志（DSH plan-mode
	// 日志即状态）：进入 plan 模式追加 log-only plan/mode 事件、批准追加
	// 退出事件；未提供实时 Mode 回调时 Mode 从日志折叠。日志是真相；
	// 重启、恢复或 fork 都从日志重建 plan 状态。
	Log *coresession.Log
}

// Denial is the closed error vocabulary for plan-mode refusals.
//
// Denial 是 plan 模式拒绝的封闭错误词汇。
type Denial struct {
	// Tool is the blocked tool's name (gate block) or the plan tool's name
	// (rejected plan).
	Tool string
	// Feedback carries the human's rejection feedback, when provided.
	Feedback string
}

// Error implements the error interface, returning the denial's feedback or
// a generic plan-mode refusal message.
// Error 实现 error 接口，返回拒绝反馈或通用的 plan 模式拒绝信息。
func (d *Denial) Error() string {
	if d.Feedback != "" {
		return fmt.Sprintf("plan mode: %s", d.Feedback)
	}
	return fmt.Sprintf("plan mode: %q has mutating effects; explore read-only and submit a plan via exit_plan_mode", d.Tool)
}

// IsDenial reports whether err is (or wraps) a plan-mode Denial.
//
// IsDenial 判断 err 是否为（或包装了）plan 模式 Denial。
func IsDenial(err error) bool {
	var d *Denial
	return errors.As(err, &d)
}

// Module gates mutating tool calls while the session is in plan mode and
// exposes the gate-crossing exit_plan_mode tool.
//
// Module 在会话处于 plan 模式时门禁变更类工具调用，并暴露跨越门禁的
// exit_plan_mode 工具。
type Module struct {
	cfg  Config
	tool kernel.Tool
}

// New validates the configuration and builds the module.
// New 校验配置并构建模块。
func New(cfg Config) (*Module, error) {
	if cfg.Ask == nil {
		return nil, fmt.Errorf("planmode: nil Ask")
	}
	if cfg.OnApprove == nil {
		return nil, fmt.Errorf("planmode: nil OnApprove")
	}
	// A Mode callback is optional when a log is present: the log folds the
	// authoritative plan state (DSH log-as-state). Without either, the
	// module cannot know the mode — fail closed.
	if cfg.Mode == nil && cfg.Log == nil {
		return nil, fmt.Errorf("planmode: nil Mode (or provide Log for log-as-state)")
	}
	if cfg.ToolName == "" {
		cfg.ToolName = DefaultToolName
	}
	m := &Module{cfg: cfg}
	ft, err := tool.ToolFromFunc(
		m.exit,
		tool.WithToolName(cfg.ToolName),
		tool.WithToolDescription("Submit the plan for approval. While the session is in plan mode every other mutating tool is blocked; a host approval exits plan mode and unlocks changes. Provide the full plan and a short summary for the approver."),
		// The submission itself mutates nothing: it reads the plan and waits
		// for a host decision (HITL interaction, no file side effects).
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	m.tool = ft
	return m, nil
}

// Mode resolves the authoritative session mode: the live callback when
// provided, otherwise the fold of the session log's plan/mode events
// (last-wins; absent events fold to normal — DSH foldPlanMode).
//
// Mode 解析权威会话模式：有实时回调时用回调，否则折叠会话日志的
// plan/mode 事件（last-wins；无事件折叠为 normal——DSH foldPlanMode）。
func (m *Module) Mode() types.SessionMode {
	if m.cfg.Mode != nil {
		return m.cfg.Mode()
	}
	if m.cfg.Log == nil {
		return types.SessionModeNormal
	}
	active := false
	for _, ev := range m.cfg.Log.Events() {
		if ev.Kind == "plan/mode" && ev.Meta != nil {
			if v, ok := ev.Meta["active"].(bool); ok {
				active = v
			}
		}
	}
	if active {
		return types.SessionModePlan
	}
	return types.SessionModeNormal
}

// MustNew builds the module, panicking on configuration errors.
// MustNew 构建模块，配置错误时 panic。
func MustNew(cfg Config) *Module {
	m, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return m
}

// Enter flips the session into plan mode. With a log configured, the
// transition is durable: a log-only plan/mode event is appended (DSH
// plan/mode log-as-state), so a restart, resume, or fork recovers plan
// state from the log. Without a log, Enter relies on the consumer's
// OnApprove-side persistence.
//
// Enter 使会话进入 plan 模式。配置日志时转换是持久的：追加 log-only
// plan/mode 事件（DSH plan/mode 日志即状态），重启、恢复或 fork 都从
// 日志重建 plan 状态。无日志时依赖消费方在 OnApprove 侧的持久化。
func (m *Module) Enter() {
	if m.cfg.Log != nil {
		_, _ = m.cfg.Log.Append(types.NewLogOnlyEvent("plan/mode", map[string]any{"active": true}))
	}
}

// Tool returns the gate-crossing exit_plan_mode tool.
// Tool 返回跨越门禁的 exit_plan_mode 工具。
func (m *Module) Tool() kernel.Tool { return m.tool }

// Register installs the plan-mode gate hook on the given runtime: while the
// session is in plan mode, mutating tool calls are blocked with a Denial.
// Register 在给定 runtime 上安装 plan 模式门禁钩子：会话处于 plan 模式
// 时，变更类工具调用以 Denial 被阻止。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnToolCall(func(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
		if info == nil || m.Mode() != types.SessionModePlan {
			return ctx, info, nil
		}
		if info.Name == m.cfg.ToolName {
			// The sanctioned gate-crossing call — the only mutation-capable
			// path in plan mode.
			return ctx, info, nil
		}
		effects := info.Effects
		if len(effects) == 0 {
			// Fail-closed: engines bypassing runtime enrichment see the
			// conservative write+exec default.
			effects = []kernel.ToolEffect{kernel.EffectWrite, kernel.EffectExec}
		}
		if hasEffect(effects, kernel.EffectWrite) ||
			hasEffect(effects, kernel.EffectExec) ||
			hasEffect(effects, kernel.EffectUserData) {
			return ctx, nil, &Denial{Tool: info.Name}
		}
		return ctx, info, nil
	})
}

// planArgs is the argument set of exit_plan_mode: plan is required, summary
// optional (FuncTool derives the schema from the json tags).
type planArgs struct {
	Plan    string `json:"plan" description:"The full plan: what will be changed and how"`
	Summary string `json:"summary,omitempty" description:"Short summary for the human approver"`
}

func (m *Module) exit(ctx context.Context, args planArgs) (string, error) {
	if strings.TrimSpace(args.Plan) == "" {
		return "", fmt.Errorf("plan mode: plan must not be empty")
	}
	if m.Mode() != types.SessionModePlan {
		return "not in plan mode — no approval needed", nil
	}
	dec, err := m.cfg.Ask(ctx, &kernel.ApprovalRequest{
		ToolName: m.cfg.ToolName,
		Args:     args.Plan,
		Reason:   args.Summary,
	})
	// A deny-with-feedback (kernel.DeniedError) is a REJECTION carrying the
	// host's reason — never an operational failure.
	if fb, ok := kernel.IsDeniedError(err); ok && dec == kernel.ApprovalDeny {
		return "", &Denial{Tool: m.cfg.ToolName, Feedback: fb}
	}
	if err != nil {
		return "", fmt.Errorf("plan mode: approval unavailable: %w", err)
	}
	switch dec {
	case kernel.ApprovalAuto, kernel.ApprovalAllowedOnce:
		// The exit is durable in the log when one is configured: the
		// plan/mode fold flips back to normal for this and future sessions.
		if m.cfg.Log != nil {
			_, _ = m.cfg.Log.Append(types.NewLogOnlyEvent("plan/mode", map[string]any{"active": false}))
		}
		m.cfg.OnApprove(args.Plan)
		return "plan approved — plan mode exited; you may now make changes", nil
	case kernel.ApprovalDeny:
		return "", &Denial{Tool: m.cfg.ToolName, Feedback: "plan rejected — revise it and resubmit"}
	default: // ApprovalAsk back from the seam: it must settle before returning
		return "", fmt.Errorf("plan mode: approval left pending")
	}
}

func hasEffect(effects []kernel.ToolEffect, target kernel.ToolEffect) bool {
	for _, e := range effects {
		if e == target {
			return true
		}
	}
	return false
}
