// ❄️ FROZEN — Stable data contract. Types, fields and semantics must not change.
package types

// Phase indicates before or after a lifecycle event.
// Deprecated: unused.
// Phase 表示生命周期事件发生前或发生后的阶段。
// Deprecated：未使用。
type Phase int

const (
	// PhaseBefore is the phase before a lifecycle event.
	// PhaseBefore 生命周期事件发生前的阶段。
	PhaseBefore Phase = iota
	// PhaseAfter is the phase after a lifecycle event.
	// PhaseAfter 生命周期事件发生后的阶段。
	PhaseAfter
)
