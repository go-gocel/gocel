// 权限预设表：把文件档位与审批策略两个旋钮打包为产品级选择器（DSH
// permission-presets 语义）。预设是"捆绑"，不自行执行任何权限——选择预设
// 后把两个值交给对应机制（FilePolicy.SetMode + 审批策略），执行与提示仍
// 由机制层各自负责。
package permission

import (
	"context"
	"errors"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

var (
	errNilFilePolicy     = errors.New("permission: nil file policy")
	errPolicyNotSettable = errors.New("permission: file policy does not support SetMode")
	errUnknownApproval   = errors.New("permission: unknown approval disposition")
)

// Preset bundles a permission tier with an approval disposition as one
// product-selectable choice (DSH permission-presets: one dropdown bundles
// the sandbox mode and the approval policy).
//
// Preset 把一个权限档位与一个审批处置捆绑为产品可选的一个选择（DSH
// permission-presets：一个下拉框捆绑沙箱模式与审批策略）。
type Preset struct {
	// Name is the stable preset id (e.g. "workspace-write").
	Name string
	// Description is the human-facing explanation for the selector UI.
	Description string
	// Mode is the permission tier this preset selects.
	Mode types.PermissionMode
	// Approval is the approval disposition this preset selects: "ask" or
	// "never" (DSH ApprovalPolicy vocabulary). Empty keeps the tier default
	// (kernel.DefaultApprovalFor).
	Approval string
}

// DefaultPresets is the shipped preset table (DSH default table):
// workspace-write + ask, and danger-full-access + never.
//
// DefaultPresets 是随包预设表（DSH 默认表）：workspace-write + ask，
// danger-full-access + never。
var DefaultPresets = []Preset{
	{
		Name:        "workspace-write",
		Description: "Writes confined to the workspace; risky calls ask the user",
		Mode:        types.PermissionWorkspaceWrite,
		Approval:    "ask",
	},
	{
		Name:        "danger-full-access",
		Description: "Full access; approval never asks (CI / unattended)",
		Mode:        types.PermissionDangerFullAccess,
		Approval:    "never",
	},
}

// FindPreset resolves a preset by name from the table. It reports whether
// the name exists.
//
// FindPreset 从表中按名称解析预设。返回名称是否存在。
func FindPreset(table []Preset, name string) (Preset, bool) {
	for _, p := range table {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// Apply wires one preset onto the file policy and returns the approval
// policy it selects. The file policy's mode is set to the preset tier; the
// approval disposition resolves to the preset's own value when present,
// otherwise the tier default (kernel.DefaultApprovalFor). A preset whose
// tier is not supported by the policy fails loudly (fail-closed).
//
// Apply 把一个预设接线到文件策略上，并返回它选择的审批策略。文件策略
// 的档位被设为预设档；审批处置在预设自带值时用自带值，否则用档位默认
// （kernel.DefaultApprovalFor）。预设档不被策略支持时显式报错
// （fail-closed）。
func (p Preset) Apply(file kernel.FilePolicy) (kernel.ApprovalPolicy, error) {
	if file == nil {
		return nil, errNilFilePolicy
	}
	setter, ok := file.(interface{ SetMode(types.PermissionMode) })
	if !ok {
		return nil, errPolicyNotSettable
	}
	// Resolve the approval BEFORE touching the file policy: an unknown
	// disposition must fail without half-applying the preset (fail-closed,
	// no partial application).
	approval, err := p.resolveApproval()
	if err != nil {
		return nil, err
	}
	setter.SetMode(p.Mode)
	return approval, nil
}

// resolveApproval maps the preset's approval disposition to a policy. It is
// the validation half of Apply — called before any state is mutated.
func (p Preset) resolveApproval() (kernel.ApprovalPolicy, error) {
	switch p.Approval {
	case "ask":
		return askApprovalPolicy{}, nil
	case "never":
		// "never" means "never prompt the host" (DSH): in-tier calls pass
		// without asking. The old mapping to NeverApprovalPolicy (deny
		// everything) made the shipped danger-full-access preset reject
		// EVERY tool call (verified defect). NeverApprovalPolicy stays for
		// delegation pinning, where deny is the intended disposition.
		return autoApprovalPolicy{}, nil
	case "":
		switch kernel.DefaultApprovalFor(p.Mode) {
		case kernel.ApprovalAuto:
			return autoApprovalPolicy{}, nil
		case kernel.ApprovalDeny:
			return NeverApprovalPolicy{}, nil
		default:
			return askApprovalPolicy{}, nil
		}
	default:
		return nil, errUnknownApproval
	}
}

// askApprovalPolicy returns ApprovalAsk for every call — the "ask" preset
// disposition (the HITL channel answers).
type askApprovalPolicy struct{}

// Decide implements kernel.ApprovalPolicy.
// Decide 实现 kernel.ApprovalPolicy：对每次调用返回 ApprovalAsk。
func (askApprovalPolicy) Decide(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
	return kernel.ApprovalAsk, nil
}

// autoApprovalPolicy allows every call — the tier-default auto disposition
// (danger-full-access).
type autoApprovalPolicy struct{}

// Decide implements kernel.ApprovalPolicy.
// Decide 实现 kernel.ApprovalPolicy：放行每次调用。
func (autoApprovalPolicy) Decide(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
	return kernel.ApprovalAuto, nil
}
