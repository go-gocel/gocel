// Package state provides type aliases re-exported from kernel for convenience.
// The canonical definitions live in github.com/go-gocel/gocel/core/kernel.
package state

import "github.com/go-gocel/gocel/core/kernel"

// StateManager provides runtime state management with change observation.
// See kernel.StateManager for the canonical interface definition.
// StateManager 提供带变更观察的运行时状态管理；规范接口定义见
// kernel.StateManager。
type StateManager = kernel.StateManager

// StateChangeFn is a callback that receives a batch of state changes.
// See kernel.StateChangeFn for the canonical definition.
// StateChangeFn 是接收一批状态变更的回调；规范定义见 kernel.StateChangeFn。
type StateChangeFn = kernel.StateChangeFn

// StateChange records a single state mutation for observation.
// See kernel.StateChange for the canonical definition.
// StateChange 记录供观察的单次状态变更；规范定义见 kernel.StateChange。
type StateChange = kernel.StateChange
