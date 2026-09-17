// Package goal provides the durable objective of a harness session: the
// state machine, the compare-and-set manager, and the storage seam. It is
// the Go counterpart of DSH's goal domain, with the semantics proven there:
//
//   - Phase is persistent (active → paused / blocked / complete); the
//     round-driver's activation ("armed") is deliberately process-local —
//     restart, resume, or a forked child never auto-continues a goal.
//   - Every mutation goes through a compare-and-set on Revision; a stale
//     revision fails loudly (ErrStaleRevision) instead of silently
//     overwriting.
//   - Two authority lines: host operations (create/pause/resume/complete)
//     require the host's initiative, while a goal round may only re-report a
//     blocker that already persisted (BlockedStreak threshold) — a round can
//     never turn a fresh goal into blocked by itself.
//   - Closed error vocabulary: ErrNotFound / ErrAlreadyExists /
//     ErrStaleRevision / ErrInvalidTransition / …
//
// Package goal 提供 harness 会话的持久化目标：状态机、CAS 管理器与存储缝。
// 它是 DSH goal 域的 Go 对应物，语义照搬被验证过的部分：
//
//   - Phase 持久（active → paused / blocked / complete）；轮次驱动的激活
//     （armed）刻意保持进程内——重启、恢复或 fork 的子代理绝不自动续跑目标。
//   - 每次变更都经过 Revision 的 CAS；stale revision 显式报错
//     （ErrStaleRevision），绝不静默覆盖。
//   - 双权限线：宿主操作（create/pause/resume/complete）需要宿主发起；
//     目标轮次只能续报已存在的阻塞（BlockedStreak 阈值）——轮次绝不能
//     自行把一个新目标打成 blocked。
//   - 封闭错误词汇：ErrNotFound / ErrAlreadyExists / ErrStaleRevision /
//     ErrInvalidTransition / …
package goal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Phase is the persistent lifecycle phase of a goal.
// Phase 是目标的持久生命周期阶段。
type Phase string

const (
	// PhaseActive marks a goal that is being worked on; rounds may be claimed.
	// PhaseActive 表示目标正在被推进，轮次可被认领。
	PhaseActive Phase = "active"
	// PhasePaused marks a goal whose work is suspended by the host; rounds
	// are not claimed.
	// PhasePaused 表示宿主暂停了目标的工作；轮次不被认领。
	PhasePaused Phase = "paused"
	// PhaseBlocked marks a goal whose work cannot proceed; BlockerReason
	// explains why.
	// PhaseBlocked 表示目标无法继续推进；原因由 BlockerReason 说明。
	PhaseBlocked Phase = "blocked"
	// PhaseComplete marks a terminal goal whose objective is achieved.
	// PhaseComplete 表示终态：目标已达成。
	PhaseComplete Phase = "complete"
)

// ErrNotFound heads the closed error vocabulary: consumers branch on these
// errors, never on string matching.
// ErrNotFound 及其后的错误构成封闭错误词汇表：消费方按错误分支，绝不匹配
// 字符串。
var (
	// ErrNotFound is returned when the goal id does not exist.
	ErrNotFound = errors.New("goal: not found")
	// ErrAlreadyExists is returned by Create on an id collision.
	ErrAlreadyExists = errors.New("goal: already exists")
	// ErrStaleRevision is returned when a mutation raced with another
	// committed revision.
	ErrStaleRevision = errors.New("goal: stale revision")
	// ErrInvalidTransition is returned when a phase transition is illegal.
	ErrInvalidTransition = errors.New("goal: invalid phase transition")
	// ErrInvalidObjective is returned for an empty objective.
	ErrInvalidObjective = errors.New("goal: invalid objective")
	// ErrInvalidMaxRounds is returned for a negative round cap.
	ErrInvalidMaxRounds = errors.New("goal: invalid max rounds")
	// ErrInvalidBlockReason is returned when blocking without a reason.
	ErrInvalidBlockReason = errors.New("goal: invalid block reason")
	// ErrMaxRoundsReached is returned by StartRound when the cap is reached.
	ErrMaxRoundsReached = errors.New("goal: max rounds reached")
)

// Goal is the durable objective document.
// Goal 是持久化目标文档。
type Goal struct {
	ID        string `json:"id"`
	Objective string `json:"objective"`
	Phase     Phase  `json:"phase"`
	// Revision increments on every committed mutation; the CAS token.
	Revision int64 `json:"revision"`
	// Rounds counts rounds claimed so far (0 = none).
	Rounds int `json:"rounds"`
	// MaxRounds caps auto-continuation rounds; 0 = unlimited.
	MaxRounds int `json:"max_rounds,omitempty"`
	// BlockedStreak counts consecutive rounds the same blocker persisted.
	BlockedStreak int `json:"blocked_streak,omitempty"`
	// BlockerReason is the current blocker, valid in PhaseBlocked.
	BlockerReason string    `json:"blocker_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Store is the storage seam. Documents are whole-value replaces; the
// Manager owns the CAS lock, so a Store must be consumed by exactly one
// Manager per process. Implementations must return copies — never share
// internal state with callers.
//
// Store 是存储缝。文档整体替换；CAS 锁由 Manager 持有，因此一个 Store
// 在单个进程内只能被一个 Manager 消费。实现必须返回副本——绝不与调用方
// 共享内部状态。
type Store interface {
	Save(ctx context.Context, g *Goal) error
	Load(ctx context.Context, id string) (*Goal, error)
	List(ctx context.Context) ([]*Goal, error)
	// Delete removes the document; deleting a missing id is a no-op.
	Delete(ctx context.Context, id string) error
}

// Manager owns goal lifecycle: creation, CAS mutation, phase transitions,
// and round claiming. The armed/disarmed activation of auto-continuation is
// deliberately NOT part of the manager — it lives in the round driver.
//
// Manager 拥有目标生命周期：创建、CAS 变更、阶段迁移与轮次认领。
// 自动续跑的 armed/disarmed 激活刻意不属于 manager——它在轮次驱动器中。
type Manager struct {
	store Store
	mu    sync.Mutex
	newID func() string
	now   func() time.Time
}

// NewManager builds a manager over the given store.
// NewManager 基于给定存储构建管理器。
func NewManager(store Store) *Manager {
	return &Manager{
		store: store,
		newID: defaultID,
		now:   time.Now,
	}
}

// Store exposes the backing store for tests and adapters.
// Store 暴露底层存储，供测试与适配器使用。
func (m *Manager) Store() Store { return m.store }

// Create registers a new active goal.
// Create 注册一个新的 active 目标。
func (m *Manager) Create(ctx context.Context, objective string, maxRounds int) (*Goal, error) {
	if strings.TrimSpace(objective) == "" {
		return nil, ErrInvalidObjective
	}
	if maxRounds < 0 {
		return nil, ErrInvalidMaxRounds
	}
	now := m.now().UTC()
	g := &Goal{
		ID: m.newID(), Objective: objective,
		Phase: PhaseActive, Revision: 1,
		MaxRounds: maxRounds, CreatedAt: now, UpdatedAt: now,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.store.Load(ctx, g.ID); err == nil {
		return nil, ErrAlreadyExists
	}
	if err := m.store.Save(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

// Get returns a copy of the goal, or ErrNotFound.
// Get 返回目标的副本；不存在时返回 ErrNotFound。
func (m *Manager) Get(ctx context.Context, id string) (*Goal, error) {
	return m.store.Load(ctx, id)
}

// List returns every goal ordered by creation time.
// List 按创建时间返回全部目标。
func (m *Manager) List(ctx context.Context) ([]*Goal, error) {
	return m.store.List(ctx)
}

// Update loads the goal, applies fn, and commits under CAS: the commit is
// rejected with ErrStaleRevision when another writer moved the revision
// since the load. fn must only mutate the goal — it must not touch the
// store. A nil-returning fn commits the mutation.
//
// Update 加载目标、应用 fn 并以 CAS 提交：若自加载以来其他写入者推进了
// revision，提交以 ErrStaleRevision 拒绝。fn 只允许修改目标——不得触碰
// 存储。fn 返回 nil 即提交变更。
func (m *Manager) Update(ctx context.Context, id string, fn func(*Goal) error) (*Goal, error) {
	if fn == nil {
		return nil, errors.New("goal: nil update function")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	rev := g.Revision
	if err := fn(g); err != nil {
		return nil, err
	}
	// Re-read: another Manager over the same store may have committed while
	// fn ran (the CAS authority is this lock, but the store is shared).
	cur, err := m.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.Revision != rev {
		return nil, ErrStaleRevision
	}
	g.Revision++
	g.UpdatedAt = m.now().UTC()
	if err := m.store.Save(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

// Complete moves active/paused/blocked goals to complete. Completing an
// already complete goal is a documented no-op (idempotent for tool callers).
// Complete 将 active/paused/blocked 目标移动到 complete；对已是 complete 的
// 目标调用是文档化的无操作（对工具调用方幂等）。
func (m *Manager) Complete(ctx context.Context, id string) (*Goal, error) {
	return m.Update(ctx, id, func(g *Goal) error {
		switch g.Phase {
		case PhaseComplete:
			return nil
		case PhaseActive, PhasePaused, PhaseBlocked:
			g.Phase = PhaseComplete
			g.BlockerReason = ""
			g.BlockedStreak = 0
			return nil
		default:
			return ErrInvalidTransition
		}
	})
}

// Pause suspends an active goal. Host authority: only the host pauses.
// Pause 挂起 active 目标。宿主权限：只有宿主能暂停。
func (m *Manager) Pause(ctx context.Context, id string) (*Goal, error) {
	return m.Update(ctx, id, func(g *Goal) error {
		if g.Phase != PhaseActive {
			return ErrInvalidTransition
		}
		g.Phase = PhasePaused
		return nil
	})
}

// Resume reactivates a paused or blocked goal; a blocked goal's streak and
// reason are cleared, so a resumed goal starts with a fresh blocker slate.
// Resume 重新激活 paused 或 blocked 目标；blocked 目标的 streak 与 reason
// 会被清空，恢复后的目标从全新的阻塞记录开始。
func (m *Manager) Resume(ctx context.Context, id string) (*Goal, error) {
	return m.Update(ctx, id, func(g *Goal) error {
		switch g.Phase {
		case PhasePaused, PhaseBlocked:
			g.Phase = PhaseActive
			g.BlockedStreak = 0
			g.BlockerReason = ""
			return nil
		default:
			return ErrInvalidTransition
		}
	})
}

// BlockOption configures a block operation's authority line.
// BlockOption 配置 block 操作的权限线。
type BlockOption func(*blockOpts)

type blockOpts struct {
	autonomous bool
	minStreak  int
}

// WithAutonomousBlock marks the block as coming from a goal round rather
// than the host. A round may only CONTINUE an existing blocker: the goal
// must already be blocked with the same reason and the streak must have
// persisted minStreak-1 prior rounds (DSH blockedAfterConsecutiveRounds,
// default 3). A round can never turn an active goal into blocked by itself.
// WithAutonomousBlock 把 block 标记为来自目标轮次而非宿主。轮次只能续报
// 已有阻塞：目标必须已处于 blocked 且原因相同，且 streak 已持续
// minStreak-1 个先前轮次（DSH blockedAfterConsecutiveRounds，默认 3）。
// 轮次绝不能自行把 active 目标打成 blocked。
func WithAutonomousBlock(minStreak int) BlockOption {
	return func(o *blockOpts) { o.autonomous = true; o.minStreak = minStreak }
}

// Block records a blocker. Host path: active → blocked (streak 1); an
// already blocked goal with the same reason extends the streak, a different
// reason restarts it. Autonomous path: only continues an existing blocker
// past the streak threshold.
// Block 记录阻塞。宿主路径：active → blocked（streak 1）；已 blocked 且
// 原因相同则延长 streak，原因不同则重新开始。自主路径：只在超过 streak
// 阈值后续报已有阻塞。
func (m *Manager) Block(ctx context.Context, id, reason string, opts ...BlockOption) (*Goal, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrInvalidBlockReason
	}
	var cfg blockOpts
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.autonomous && cfg.minStreak < 1 {
		return nil, errors.New("goal: invalid autonomous block threshold")
	}
	return m.Update(ctx, id, func(g *Goal) error {
		switch {
		case cfg.autonomous:
			if g.Phase != PhaseBlocked || g.BlockerReason != reason || g.BlockedStreak < cfg.minStreak-1 {
				return ErrInvalidTransition
			}
			g.BlockedStreak++
		case g.Phase == PhaseActive:
			g.Phase = PhaseBlocked
			g.BlockerReason = reason
			g.BlockedStreak = 1
		case g.Phase == PhaseBlocked:
			if g.BlockerReason == reason {
				g.BlockedStreak++
			} else {
				g.BlockerReason = reason
				g.BlockedStreak = 1
			}
		default:
			return ErrInvalidTransition
		}
		return nil
	})
}

// StartRound claims the next round of an active goal under CAS: Rounds
// increments and the committed goal carries the round number. It fails with
// ErrMaxRoundsReached when the cap is exhausted, and with
// ErrInvalidTransition when the goal is not active (paused/blocked goals
// claim no rounds).
//
// StartRound 在 CAS 下认领 active 目标的下一轮：Rounds 递增，提交的目标
// 携带轮次号。达到上限返回 ErrMaxRoundsReached；目标非 active 返回
// ErrInvalidTransition（paused/blocked 不认领轮次）。
func (m *Manager) StartRound(ctx context.Context, id string) (*Goal, error) {
	return m.Update(ctx, id, func(g *Goal) error {
		if g.Phase != PhaseActive {
			return ErrInvalidTransition
		}
		if g.MaxRounds > 0 && g.Rounds >= g.MaxRounds {
			return ErrMaxRoundsReached
		}
		g.Rounds++
		return nil
	})
}

// Clear removes the goal. Deleting a missing goal is a no-op. DSH keeps a
// tombstone for audit; the audit trail here comes with the session event
// log, which lands later.
// Clear 删除目标。删除缺失的目标是无操作（DSH 为审计保留墓碑；这里的审计
// 轨迹随会话事件日志而来，稍后落地）。
func (m *Manager) Clear(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.Delete(ctx, id)
}

func defaultID() string {
	return "goal-" + time.Now().UTC().Format("20060102T150405") + "-" + randomSuffix()
}
