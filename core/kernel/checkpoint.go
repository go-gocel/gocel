package kernel

import (
	"context"

	"github.com/go-gocel/gocel/core/types"
)

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// CheckpointStore persists checkpoints for interrupt/resume lifecycle.
//
// CheckpointStore 持久化检查点，支持中断/恢复生命周期。
type CheckpointStore interface {
	Save(ctx context.Context, cp *types.Checkpoint) error
	Load(ctx context.Context, id string) (*types.Checkpoint, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]string, error)
}
