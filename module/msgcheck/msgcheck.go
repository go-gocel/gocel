// Package msgcheck audits the message list before every model call.
// It currently enforces the single-system invariant (replacing the removed
// types.ValidateSingleSystem); future checks (secret/key detection, message
// safety) extend here.
//
// Package msgcheck 在每次模型调用前审计消息列表。当前实现单 system 不变量
// 校验（替代已删除的 types.ValidateSingleSystem）；后续的消息安全、密钥
// 检测等检查在此扩展。
package msgcheck

import (
	"context"
	"fmt"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Module audits messages before model calls.
// Module 在模型调用前审计消息。
type Module struct{}

// New creates the audit module.
// New 创建审计模块。
func New() *Module { return &Module{} }

// Register implements kernel.Module：注册 OnModelCall 钩子。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnModelCall(m.onModelCall)
}

// onModelCall 在模型调用前校验消息合法性。违规返回 error 中止模型调用。
func (m *Module) onModelCall(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	if info == nil {
		return ctx, info, nil
	}
	if err := validateSingleSystem(info.Messages); err != nil {
		return ctx, nil, err
	}
	return ctx, info, nil
}

// validateSingleSystem 校验「至多一条 system 且位于第 0 位」。
// 后续可在此追加密钥检测、消息安全等检查项。
func validateSingleSystem(msgs []*types.Message) error {
	for i, m := range msgs {
		if m == nil || m.Role != types.RoleSystem {
			continue
		}
		if i != 0 {
			return fmt.Errorf("msgcheck: system message at index %d (must be index 0)", i)
		}
	}
	return nil
}
